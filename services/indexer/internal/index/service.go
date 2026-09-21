// Package index transforma eventos y alertas en documentos de Elasticsearch.
//
// El indexer consume de Kafka igual que el persister, pero escribe en
// Elasticsearch en lugar de PostgreSQL. Sustituye al ETL por lotes del sistema
// Java, que copiaba de PostgreSQL a Elasticsearch una vez por hora: una
// busqueda podia no encontrar un incidente ocurrido cincuenta minutos antes
// (ver ADR-009).
//
// Al escribir fuera de la LAN, este servicio esta —como el persister— sobre la
// frontera de privacidad, de modo que tambien tokeniza la placa vehicular antes
// de que el dato cruce (ver ADR-007).
package index

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"ciudadinteligente/indexer/internal/broker"
	"ciudadinteligente/indexer/internal/canonical"
	"ciudadinteligente/indexer/internal/esearch"
	"ciudadinteligente/indexer/internal/obs"
	"ciudadinteligente/indexer/internal/privacy"
)

// Service indexa lotes de mensajes.
type Service struct {
	es          *esearch.Client
	publicador  *broker.Publisher
	validador   *canonical.Validator
	tokenizador *privacy.Tokenizador
	metrics     *obs.Metrics
	log         *slog.Logger
}

// New construye el servicio.
func New(es *esearch.Client, p *broker.Publisher, v *canonical.Validator,
	tok *privacy.Tokenizador, m *obs.Metrics, log *slog.Logger) *Service {
	return &Service{es: es, publicador: p, validador: v,
		tokenizador: tok, metrics: m, log: log}
}

// IndexarEventos transforma e indexa un lote de eventos.
func (s *Service) IndexarEventos(ctx context.Context, mensajes []*broker.Mensaje) error {
	if len(mensajes) == 0 {
		return nil
	}

	docs := make([]esearch.Documento, 0, len(mensajes))
	origen := make(map[string]*broker.Mensaje, len(mensajes))

	for _, m := range mensajes {
		if err := s.validador.ValidateEventBytes(m.Valor); err != nil {
			s.metrics.Rechazados.WithLabelValues("evento", "contrato").Inc()
			s.aDLQ(ctx, "indexacion_contrato", detalleDeValidacion(err), m)
			continue
		}

		ev, err := canonical.ParseEvent(m.Valor)
		if err != nil {
			s.metrics.Rechazados.WithLabelValues("evento", "mapeo").Inc()
			s.aDLQ(ctx, "indexacion_mapeo", err.Error(), m)
			continue
		}

		momento, err := ev.TimestampUTC()
		if err != nil {
			s.metrics.Rechazados.WithLabelValues("evento", "timestamp").Inc()
			s.aDLQ(ctx, "indexacion_timestamp", err.Error(), m)
			continue
		}

		docs = append(docs, esearch.Documento{
			Indice: indiceMensual("events", momento),
			ID:     ev.EventID,
			Cuerpo: documentoDeEvento(ev, s.tokenizador),
		})
		origen[ev.EventID] = m

		// Distancia entre el momento del evento y su disponibilidad para
		// busqueda. Es la metrica que cuantifica la mejora sobre el ETL
		// horario del sistema Java, donde este valor se acercaba a una hora.
		s.metrics.RetrasoIndice.Observe(time.Since(momento).Seconds())
	}

	return s.enviar(ctx, "eventos", docs, origen, mensajes)
}

// IndexarAlertas transforma e indexa un lote de alertas.
func (s *Service) IndexarAlertas(ctx context.Context, mensajes []*broker.Mensaje) error {
	if len(mensajes) == 0 {
		return nil
	}

	docs := make([]esearch.Documento, 0, len(mensajes))
	origen := make(map[string]*broker.Mensaje, len(mensajes))

	for _, m := range mensajes {
		if err := s.validador.ValidateAlertBytes(m.Valor); err != nil {
			s.metrics.Rechazados.WithLabelValues("alerta", "contrato").Inc()
			s.aDLQ(ctx, "indexacion_alerta_contrato", detalleDeValidacion(err), m)
			continue
		}

		a, err := canonical.ParseAlert(m.Valor)
		if err != nil {
			s.metrics.Rechazados.WithLabelValues("alerta", "mapeo").Inc()
			s.aDLQ(ctx, "indexacion_alerta_mapeo", err.Error(), m)
			continue
		}

		creada, err := time.Parse(time.RFC3339Nano, a.CreatedAt)
		if err != nil {
			s.metrics.Rechazados.WithLabelValues("alerta", "timestamp").Inc()
			s.aDLQ(ctx, "indexacion_alerta_timestamp", err.Error(), m)
			continue
		}

		docs = append(docs, esearch.Documento{
			Indice: indiceMensual("alerts", creada),
			ID:     a.AlertID,
			Cuerpo: a,
		})
		origen[a.AlertID] = m
	}

	return s.enviar(ctx, "alertas", docs, origen, mensajes)
}

// enviar indexa el lote y reparte los resultados.
func (s *Service) enviar(ctx context.Context, flujo string, docs []esearch.Documento,
	origen map[string]*broker.Mensaje, mensajes []*broker.Mensaje) error {

	if len(docs) == 0 {
		return nil
	}

	inicio := time.Now()
	res, err := s.es.Bulk(ctx, docs)
	duracion := time.Since(inicio)

	if err != nil {
		// Fallo de transporte: afecta al lote entero. Se devuelve el error
		// para que el llamante no confirme offsets y se reintente.
		s.log.Error("fallo la indexacion del lote, se reintentara",
			slog.String("flujo", flujo),
			slog.Int("documentos", len(docs)),
			slog.String("error", err.Error()))
		return err
	}

	s.metrics.LotesIndexados.WithLabelValues(flujo).Inc()
	s.metrics.TamanoLote.WithLabelValues(flujo).Observe(float64(len(docs)))
	s.metrics.DuracionIndexacion.WithLabelValues(flujo).Observe(duracion.Seconds())
	s.metrics.Indexados.WithLabelValues(flujo).Add(float64(res.Indexados))

	// Un documento rechazado por Elasticsearch —un mapeo incompatible, por
	// ejemplo— no se resuelve reintentando: es un fallo permanente y va a la
	// DLQ, siguiendo la clasificacion del ADR-006.
	for _, f := range res.Fallidos {
		s.metrics.Rechazados.WithLabelValues(flujo, "elasticsearch").Inc()
		if m, ok := origen[f.ID]; ok {
			s.aDLQ(ctx, "indexacion_rechazada", f.Motivo, m)
		}
		s.log.Warn("elasticsearch rechazo un documento",
			slog.String("flujo", flujo),
			slog.String("id", f.ID),
			slog.String("motivo", f.Motivo))
	}

	s.log.Info("lote indexado",
		slog.String("flujo", flujo),
		slog.Int("indexados", res.Indexados),
		slog.Int("rechazados", len(res.Fallidos)),
		slog.Float64("duracion_s", duracion.Seconds()))

	return nil
}

// documentoDeEvento adapta el evento canonico a la forma que espera el indice.
//
// Dos transformaciones que no son cosmeticas:
//
//   - geo pasa de {zone, lat, lon} a un geo_point {lat, lon}, con la zona
//     promovida a campo propio. Elasticsearch exige esa forma para poder
//     filtrar por distancia y dibujar mapas de calor; el ETL del sistema Java
//     enviaba el objeto completo y el campo quedaba inutilizable como
//     geolocalizacion.
//   - la placa vehicular se sustituye por su seudonimo antes de cruzar hacia
//     la nube.
func documentoDeEvento(ev *canonical.Event, tok *privacy.Tokenizador) map[string]any {
	doc := map[string]any{
		"event_id":      ev.EventID,
		"event_type":    ev.EventType,
		"event_version": ev.EventVersion,
		"producer":      ev.Producer,
		"source":        ev.Source,
		"timestamp":     ev.Timestamp,
		"partition_key": ev.PartitionKey,
		"zone":          ev.Geo.Zone,
		"severity":      ev.Severity,
		"payload":       tok.TokenizarPayload(ev.Payload),
	}

	if ev.CorrelationID != "" {
		doc["correlation_id"] = ev.CorrelationID
	}
	if ev.TraceID != "" {
		doc["trace_id"] = ev.TraceID
	}

	// geo_point solo se emite si hay coordenadas completas: un punto a medias
	// haria fallar la indexacion del documento entero.
	if ev.Geo.Lat != nil && ev.Geo.Lon != nil {
		doc["geo"] = map[string]float64{"lat": *ev.Geo.Lat, "lon": *ev.Geo.Lon}
	}

	return doc
}

// indiceMensual construye el nombre del indice a partir de la fecha.
//
// Un indice por mes, no uno unico. La razon es la politica de retencion del
// ADR-007: eliminar datos antiguos se convierte en borrar un indice completo,
// que es una operacion inmediata, en lugar de un DELETE BY QUERY sobre
// millones de documentos, que es caro y deja el indice fragmentado.
func indiceMensual(prefijo string, momento time.Time) string {
	return prefijo + "-" + momento.UTC().Format("2006.01")
}

func (s *Service) aDLQ(ctx context.Context, motivo string, detalle any, m *broker.Mensaje) {
	limpieza, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.publicador.PublicarDLQ(limpieza, motivo, detalle, m); err != nil {
		s.log.Error("no se pudo escribir en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}
	s.metrics.DLQPublicados.WithLabelValues(motivo).Inc()
}

func detalleDeValidacion(err error) any {
	var verr *canonical.ValidationError
	if errors.As(err, &verr) {
		return verr.Fields
	}
	return err.Error()
}
