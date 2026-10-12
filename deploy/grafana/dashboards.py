# -*- coding: utf-8 -*-
"""Genera los tableros de Grafana provisionados del proyecto.

    python deploy/grafana/dashboards.py

Se generan desde codigo y no a mano porque el JSON de Grafana es largo y
repetitivo, y un tablero mal formado no falla al arrancar: simplemente no
aparece, que es la peor forma de descubrirlo en una demostracion en vivo.
"""
import io
import json
import os
import sys

DESTINO = sys.argv[1] if len(sys.argv) > 1 else os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "provisioning", "dashboards")

PROM = {"type": "prometheus", "uid": "prometheus-ciudad"}

# Prometheus 3 normaliza las etiquetas le de los histogramas a su
# representacion canonica: el bucket que el correlator expone como le="2" se
# almacena como le="2.0". Un selector literal le="2" no encuentra nada y el
# panel del SLO llega vacio. Esta expresion acepta las dos formas.
BUCKET_2S = r'le=~"2(\\.0)?"' 
PG = {"type": "grafana-postgresql-datasource", "uid": "postgres-ciudad"}

VERDE, AMARILLO, ROJO = "green", "yellow", "red"


def umbrales(*pasos):
    """pasos: (color, valor) con valor None para el escalon base."""
    return {"mode": "absolute",
            "steps": [{"color": c, "value": v} for c, v in pasos]}


def campos(unit=None, decimals=None, thr=None, mode="thresholds",
           min_=None, max_=None, custom=None, mappings=None, overrides=None):
    d = {"color": {"mode": mode}, "mappings": mappings or []}
    if unit:
        d["unit"] = unit
    if decimals is not None:
        d["decimals"] = decimals
    if thr:
        d["thresholds"] = thr
    else:
        d["thresholds"] = umbrales((VERDE, None))
    if min_ is not None:
        d["min"] = min_
    if max_ is not None:
        d["max"] = max_
    if custom:
        d["custom"] = custom
    return {"defaults": d, "overrides": overrides or []}


LINEA = {"drawStyle": "line", "lineWidth": 2, "fillOpacity": 12,
         "showPoints": "never", "spanNulls": True,
         "scaleDistribution": {"type": "linear"},
         "axisPlacement": "auto", "gradientMode": "opacity"}


def prom(expr, leyenda=None, refId="A", instant=False):
    t = {"refId": refId, "datasource": PROM, "expr": expr,
         "editorMode": "code", "range": not instant, "instant": instant}
    if leyenda:
        t["legendFormat"] = leyenda
    return t


def sql(consulta, refId="A", formato="table"):
    return {"refId": refId, "datasource": PG, "format": formato,
            "rawQuery": True, "editorMode": "code",
            "rawSql": consulta}


_id = [0]


def panel(tipo, titulo, x, y, w, h, ds, targets, fc, options, desc=None):
    _id[0] += 1
    p = {"id": _id[0], "type": tipo, "title": titulo,
         "gridPos": {"h": h, "w": w, "x": x, "y": y},
         "datasource": ds, "targets": targets,
         "fieldConfig": fc, "options": options}
    if desc:
        p["description"] = desc
    return p


OPT_STAT = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "orientation": "auto", "textMode": "auto", "colorMode": "value",
            "graphMode": "area", "justifyMode": "auto",
            "wideLayout": True, "showPercentChange": False}

OPT_SERIE = {"legend": {"displayMode": "list", "placement": "bottom",
                        "showLegend": True, "calcs": []},
             "tooltip": {"mode": "multi", "sort": "desc"}}

OPT_TABLA = {"showHeader": True, "cellHeight": "sm",
             "footer": {"show": False, "reducer": ["sum"], "countRows": False, "fields": ""}}


def barra(orientacion="horizontal", valores=False):
    return {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": valores},
            "orientation": orientacion, "displayMode": "gradient",
            "valueMode": "color", "showUnfilled": True, "minVizWidth": 8,
            "minVizHeight": 16, "maxVizHeight": 300, "namePlacement": "auto",
            "sizing": "auto"}


def tablero(uid, titulo, descripcion, etiquetas, paneles, variables,
            refresco="10s", desde="now-1h"):
    return {
        "uid": uid,
        "title": titulo,
        "description": descripcion,
        "tags": etiquetas,
        "timezone": "browser",
        "schemaVersion": 39,
        "version": 1,
        "editable": True,
        "refresh": refresco,
        "time": {"from": desde, "to": "now"},
        "timepicker": {"refresh_intervals": ["5s", "10s", "30s", "1m", "5m", "15m", "1h"]},
        "templating": {"list": variables},
        "annotations": {"list": []},
        "panels": paneles,
    }


ZONAS = ["zona_1", "zona_2", "zona_3", "zona_4", "zona_5", "zona_6", "zona_7",
         "zona_8", "zona_9", "zona_10", "zona_11", "zona_12", "zona_13",
         "zona_14", "zona_15", "zona_16", "zona_17", "zona_18", "zona_19",
         "zona_21", "zona_24", "zona_25"]


def var_zona(comodin):
    # Variable CUSTOM, no de consulta. El catalogo de zonas es un hecho del
    # municipio, no un dato que haya que descubrir en los eventos: la zona 16
    # existe aunque hoy no haya reportado nada. Ademas una variable de consulta
    # que falla deja el tablero entero sin resolver y Grafana no lanza NINGUNA
    # consulta de panel, ni siquiera de los paneles que no usan la variable. El
    # sintoma es "No data" en todo con el origen de datos sano, y lo delata el
    # triangulo rojo junto al selector de zona. Una lista fija no puede fallar.
    #
    # comodin es el valor de "All": "%" para LIKE de SQL, ".*" para PromQL.
    return {
        "name": "zona", "label": "Zona", "type": "custom",
        "query": ",".join(ZONAS),
        "options": [{"selected": False, "text": z, "value": z} for z in ZONAS],
        "includeAll": True, "allValue": comodin, "multi": False,
        "current": {"selected": True, "text": "All", "value": "$__all"},
    }


def var_prom_zona():
    return var_zona(".*")


def var_pg_zona():
    return var_zona("%")


# ===========================================================================
# 1. Operacion en tiempo real  (Prometheus)
# ===========================================================================
_id[0] = 0
p1 = []

p1.append(panel("stat", "Eventos por segundo", 0, 0, 6, 4, PROM,
                [prom('sum(rate(ingestor_events_ingested_total{zone=~"$zona"}[5m]))')],
                campos(unit="ops", decimals=1), OPT_STAT,
                "Ritmo de ingesta aceptada. El volumen estimado en operacion es de 20 a 100 ev/s."))

p1.append(panel("stat", "Alertas en la ultima hora", 6, 0, 6, 4, PROM,
                [prom('sum(increase(correlator_alerts_generated_total{zone=~"$zona"}[1h]))')],
                campos(unit="short", decimals=0), OPT_STAT))

p1.append(panel("stat", "Latencia p99 de deteccion", 12, 0, 6, 4, PROM,
                [prom('histogram_quantile(0.99, sum(rate(correlator_correlation_latency_seconds_bucket[5m])) by (le))')],
                campos(unit="s", decimals=3,
                       thr=umbrales((VERDE, None), (AMARILLO, 1), (ROJO, 2))),
                OPT_STAT,
                "Umbral del SLO: 2 s. El amarillo marca la mitad del presupuesto consumido."))

p1.append(panel("stat", "Servicios activos", 18, 0, 6, 4, PROM,
                [prom('count(up{job=~"ingestor|correlator|persister|indexer|jobs"} == 1)')],
                campos(unit="short", decimals=0,
                       thr=umbrales((ROJO, None), (AMARILLO, 4), (VERDE, 5))),
                OPT_STAT, "De cinco. Cual falta se ve en el panel de estado."))

p1.append(panel("timeseries", "Eventos ingeridos por tipo de sensor", 0, 4, 12, 9, PROM,
                [prom('sum by (event_type) (rate(ingestor_events_ingested_total{zone=~"$zona"}[$__rate_interval]))',
                      "{{event_type}}")],
                campos(unit="ops", decimals=2, mode="palette-classic", custom=LINEA),
                OPT_SERIE,
                "Los cinco tipos de sensor urbano simulados: boton de panico, LPR, velocidad, acustico y reporte ciudadano."))

p1.append(panel("timeseries", "Alertas correlacionadas por tipo", 12, 4, 12, 9, PROM,
                [prom('sum by (type) (rate(correlator_alerts_generated_total{zone=~"$zona"}[$__rate_interval])) * 60',
                      "{{type}}")],
                campos(unit="short", decimals=2, mode="palette-classic",
                       custom=dict(LINEA, axisLabel="alertas/min")),
                OPT_SERIE,
                "possible_robbery, accident, fire y traffic_speed_violation."))

# topk y no la lista completa: una ciudad tiene decenas de zonas y un panel que
# las enumere todas deja de leerse en cuanto hay trafico. Lo que importa aqui no
# es el censo de zonas sino cuales tienen la ventana mas cargada, porque es la
# que encarece cada evaluacion.
p1.append(panel("bargauge", "Zonas con la ventana de correlacion mas cargada", 0, 13, 8, 8, PROM,
                [prom('topk(10, correlator_window_events)', "{{zone}}")],
                campos(unit="short", decimals=0, min_=0,
                       thr=umbrales((VERDE, None), (AMARILLO, 2000), (ROJO, 5000))),
                barra(),
                "Las diez zonas con mas eventos vigentes. Una ventana desproporcionada encarece cada evaluacion: por encima de 5000 salta VentanaDeCorrelacionDesproporcionada."))

p1.append(panel("timeseries", "Eventos rechazados, por motivo", 8, 13, 8, 8, PROM,
                [prom('sum by (reason) (rate(ingestor_events_rejected_total[$__rate_interval]))',
                      "{{reason}}")],
                campos(unit="ops", decimals=3, mode="palette-classic", custom=LINEA),
                OPT_SERIE,
                "Un duplicado es comportamiento normal del reintento del productor; los demas motivos no lo son."))

p1.append(panel("timeseries", "Supresion y falsos positivos", 16, 13, 8, 8, PROM,
                [prom('sum(rate(correlator_alerts_suppressed_total[$__rate_interval])) * 60', "suprimidas/min", "A"),
                 prom('sum(rate(correlator_false_positive_candidates_total[$__rate_interval])) * 60', "candidatos a falso positivo/min", "B")],
                campos(unit="short", decimals=2, mode="palette-classic", custom=LINEA),
                OPT_SERIE,
                "Coincidencias parciales cuya ventana expiro sin completarse: el caso 'zona 10, 17:22' del enunciado."))

p1.append(panel("timeseries", "Retraso de archivado e indexacion (p95)", 0, 21, 12, 8, PROM,
                [prom('histogram_quantile(0.95, sum(rate(persister_retraso_archivo_seconds_bucket[$__rate_interval])) by (le))', "PostgreSQL", "A"),
                 prom('histogram_quantile(0.95, sum(rate(indexer_retraso_indice_seconds_bucket[$__rate_interval])) by (le))', "Elasticsearch", "B")],
                campos(unit="s", decimals=2, mode="palette-classic", custom=LINEA),
                OPT_SERIE,
                "Tiempo entre el timestamp del evento y su escritura fuera de la LAN. Fuera del camino critico: un retraso aqui no afecta a la deteccion."))

p1.append(panel("bargauge", "Mensajes enviados a la cola de fallidos (24 h)", 12, 21, 6, 8, PROM,
                [prom('sum(increase(ingestor_dlq_published_total[24h]))', "ingestor", "A"),
                 prom('sum(increase(correlator_dlq_published_total[24h]))', "correlator", "B"),
                 prom('sum(increase(persister_dlq_publicados_total[24h]))', "persister", "C"),
                 prom('sum(increase(indexer_dlq_publicados_total[24h]))', "indexer", "D")],
                campos(unit="short", decimals=0, min_=0,
                       thr=umbrales((VERDE, None), (AMARILLO, 1), (ROJO, 10))),
                barra(),
                "Cualquier valor distinto de cero merece revision: significa que un mensaje no se pudo procesar y quedo apartado."))

p1.append(panel("stat", "Estado de cada servicio", 18, 21, 6, 8, PROM,
                [prom('up{job=~"ingestor|correlator|persister|indexer|jobs"}', "{{job}}")],
                campos(unit="short", decimals=0,
                       thr=umbrales((ROJO, None), (VERDE, 1)),
                       mappings=[{"type": "value", "options": {
                           "0": {"text": "caido", "index": 0},
                           "1": {"text": "activo", "index": 1}}}]),
                dict(OPT_STAT, colorMode="background", textMode="value_and_name",
                     graphMode="none", orientation="vertical"),
                "Si cae el ingestor o el correlator, la ciudad deja de detectar incidentes. Si cae el persister o el indexer, solo se retrasa el archivo."))

d1 = tablero("ciudad-operacion", "Ciudad Inteligente — Operacion en tiempo real",
             "Flujo de eventos y alertas segun las metricas que exponen los cinco microservicios. Es el tablero de la jornada de simulacion.",
             ["ciudad-inteligente", "tiempo-real"], p1, [var_prom_zona()],
             refresco="10s", desde="now-1h")

# ===========================================================================
# 2. Incidentes y mapa de calor  (PostgreSQL)
# ===========================================================================
_id[0] = 100
p2 = []

FILTRO_EV = "$__timeFilter(ts_utc) AND zone LIKE '$zona'"
FILTRO_AL = "$__timeFilter(created_at) AND zone LIKE '$zona'"
FILTRO_AL_A = "$__timeFilter(a.created_at) AND a.zone LIKE '$zona'"

# Una alerta no guarda coordenadas: guarda los event_id que la sustentan. La
# ubicacion de un incidente es, por tanto, la de sus evidencias.
#
# Se promedia en lugar de tomar el primer evento porque una alerta correlaciona
# varios sensores proximos —el boton de panico, la camara, el microfono— y
# ninguno esta exactamente donde ocurrio el hecho: el punto medio se aproxima
# mas al incidente que cualquiera de los aparatos por separado.
UBICACION = (
    "LEFT JOIN LATERAL (\n"
    "  SELECT round(avg(e.geo_lat), 6) AS lat,\n"
    "         round(avg(e.geo_lon), 6) AS lon\n"
    "  FROM jsonb_array_elements_text(coalesce(a.evidence, '[]'::jsonb)) AS ev(id)\n"
    "  JOIN events e ON e.event_id = ev.id::uuid\n"
    "  WHERE e.geo_lat IS NOT NULL\n"
    ") u ON TRUE")

p2.append(panel("stat", "Eventos en el periodo", 0, 0, 6, 4, PG,
                [sql("SELECT count(*) AS eventos FROM events WHERE " + FILTRO_EV)],
                campos(unit="short", decimals=0), OPT_STAT))

p2.append(panel("stat", "Alertas correlacionadas", 6, 0, 6, 4, PG,
                [sql("SELECT count(*) AS alertas FROM alerts WHERE " + FILTRO_AL)],
                campos(unit="short", decimals=0), OPT_STAT))

p2.append(panel("stat", "Alertas criticas (confianza mayor o igual a 0.9)", 12, 0, 6, 4, PG,
                [sql("SELECT count(*) AS criticas FROM alerts WHERE " + FILTRO_AL + " AND score >= 0.9")],
                campos(unit="short", decimals=0,
                       thr=umbrales((VERDE, None), (AMARILLO, 1), (ROJO, 10))),
                OPT_STAT,
                "Confianza 0.95: incendio confirmado, o accidente con caida de velocidad. 0.90: exceso de velocidad sostenido. 0.85: robo, o accidente sin caida confirmada."))

p2.append(panel("stat", "Zonas con actividad", 18, 0, 6, 4, PG,
                [sql("SELECT count(DISTINCT zone) AS zonas FROM events WHERE $__timeFilter(ts_utc)")],
                campos(unit="short", decimals=0), OPT_STAT))

p2.append(panel("geomap", "Mapa de calor de eventos", 0, 4, 14, 13, PG,
                [sql("SELECT geo_lat AS latitude, geo_lon AS longitude, count(*) AS peso\n"
                     "FROM events\n"
                     "WHERE " + FILTRO_EV + "\n"
                     "  AND geo_lat IS NOT NULL AND geo_lon IS NOT NULL\n"
                     "GROUP BY geo_lat, geo_lon")],
                campos(unit="short", decimals=0, mode="continuous-GrYlRd"),
                {"view": {"id": "coords", "lat": 14.6349, "lon": -90.5069, "zoom": 11,
                          "allLayers": True},
                 # OpenStreetMap explicito, no "default". El mapa base por
                 # defecto de Grafana 12 es CARTO, que exige una clave de API:
                 # sin ella el panel se muestra con "API KEY REQUIRED" escrito
                 # en diagonal sobre todo el mapa, y en una demostracion eso es
                 # lo primero que ve el evaluador. OSM no pide clave.
                 "basemap": {"type": "osm-standard", "name": "OpenStreetMap", "config": {}},
                 "layers": [
                     {"type": "heatmap", "name": "Densidad de eventos",
                      "tooltip": True, "opacity": 0.9,
                      "location": {"mode": "coords", "latitude": "latitude",
                                   "longitude": "longitude"},
                      "config": {"weight": {"field": "peso", "fixed": 1, "min": 0, "max": 1},
                                 "radius": 18, "blur": 14}},
                     {"type": "markers", "name": "Puntos",
                      "tooltip": True, "opacity": 0.7,
                      "location": {"mode": "coords", "latitude": "latitude",
                                   "longitude": "longitude"},
                      "config": {"showLegend": False,
                                 "style": {"color": {"field": "peso", "fixed": "dark-orange"},
                                           "opacity": 0.5,
                                           "size": {"field": "peso", "fixed": 4, "min": 3, "max": 12},
                                           "symbol": {"mode": "fixed", "fixed": "img/icons/marker/circle.svg"},
                                           "symbolAlign": {"horizontal": "center", "vertical": "center"},
                                           "textConfig": {"fontSize": 12, "offsetX": 0, "offsetY": 0,
                                                          "textAlign": "center", "textBaseline": "middle"}}}}],
                 "controls": {"showZoom": True, "mouseWheelZoom": True,
                              "showAttribution": True, "showScale": False,
                              "showMeasure": False, "showDebug": False},
                 "tooltip": {"mode": "details"}},
                "Densidad de eventos sobre el area metropolitana. Centrado en la Ciudad de Guatemala (14.6349, -90.5069)."))

p2.append(panel("table", "Alertas criticas del periodo", 14, 4, 10, 13, PG,
                [sql("SELECT created_at AS \"Hora\",\n"
                     "       type         AS \"Tipo\",\n"
                     "       zone         AS \"Zona\",\n"
                     "       round(score, 2) AS \"Confianza\",\n"
                     "       jsonb_array_length(evidence) AS \"Eventos que la sustentan\"\n"
                     "FROM alerts\n"
                     "WHERE " + FILTRO_AL + " AND score >= 0.9\n"
                     "ORDER BY created_at DESC\n"
                     "LIMIT 100")],
                campos(unit="short",
                       custom={"align": "auto", "cellOptions": {"type": "auto"},
                               "inspect": False, "filterable": True},
                       overrides=[{"matcher": {"id": "byName", "options": "Confianza"},
                                   "properties": [
                                       {"id": "custom.cellOptions",
                                        "value": {"type": "color-background", "mode": "gradient"}},
                                       {"id": "thresholds",
                                        "value": umbrales((AMARILLO, None), (ROJO, 0.95))},
                                       {"id": "max", "value": 1},
                                       {"id": "min", "value": 0}]}]),
                OPT_TABLA,
                "La confianza es un numero real, no un entero. Redondearla haria que 0.85, 0.90 y 0.95 se mostraran las tres como 1."))

p2.append(panel("timeseries", "Eventos a lo largo del tiempo, por tipo", 0, 17, 12, 8, PG,
                [sql("SELECT $__timeGroupAlias(ts_utc, $__interval),\n"
                     "       event_type AS metric,\n"
                     "       count(*)   AS value\n"
                     "FROM events\n"
                     "WHERE " + FILTRO_EV + "\n"
                     "GROUP BY 1, 2\n"
                     "ORDER BY 1", formato="time_series")],
                campos(unit="short", decimals=0, mode="palette-classic",
                       custom=dict(LINEA, drawStyle="bars", fillOpacity=70,
                                   stacking={"mode": "normal", "group": "A"})),
                OPT_SERIE))

p2.append(panel("piechart", "Reparto por tipo de sensor", 12, 17, 6, 8, PG,
                [sql("SELECT event_type AS \"Tipo\", count(*) AS \"Eventos\"\n"
                     "FROM events WHERE " + FILTRO_EV + "\n"
                     "GROUP BY 1 ORDER BY 2 DESC")],
                campos(unit="short", decimals=0, mode="palette-classic"),
                {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": True},
                 "pieType": "donut", "displayLabels": ["percent"],
                 "tooltip": {"mode": "single", "sort": "none"},
                 "legend": {"displayMode": "list", "placement": "right",
                            "showLegend": True, "values": []}}))

p2.append(panel("barchart", "Alertas por tipo", 18, 17, 6, 8, PG,
                [sql("SELECT type AS \"Tipo\", count(*) AS \"Alertas\"\n"
                     "FROM alerts WHERE " + FILTRO_AL + "\n"
                     "GROUP BY 1 ORDER BY 2 DESC")],
                campos(unit="short", decimals=0, mode="palette-classic",
                       custom={"lineWidth": 1, "fillOpacity": 80, "axisPlacement": "auto",
                               "gradientMode": "none", "thresholdsStyle": {"mode": "off"}}),
                {"orientation": "horizontal", "xTickLabelRotation": 0,
                 "xTickLabelSpacing": 0, "showValue": "auto", "stacking": "none",
                 "groupWidth": 0.7, "barWidth": 0.8, "barRadius": 0,
                 "fullHighlight": False,
                 "tooltip": {"mode": "single", "sort": "none"},
                 "legend": {"displayMode": "list", "placement": "bottom", "showLegend": False}}))

p2.append(panel("table", "Lista de incidentes correlacionados", 0, 25, 24, 10, PG,
                [sql("SELECT a.created_at   AS \"Hora\",\n"
                     "       a.type         AS \"Tipo de alerta\",\n"
                     "       a.zone         AS \"Zona\",\n"
                     "       round(a.score, 2) AS \"Confianza\",\n"
                     "       u.lat          AS \"Latitud\",\n"
                     "       u.lon          AS \"Longitud\",\n"
                     "       a.window_start AS \"Inicio de ventana\",\n"
                     "       a.window_end   AS \"Fin de ventana\",\n"
                     "       jsonb_array_length(a.evidence) AS \"Evidencias\",\n"
                     "       a.correlation_id AS \"Correlacion\"\n"
                     "FROM alerts a\n"
                     + UBICACION + "\n"
                     "WHERE " + FILTRO_AL_A + "\n"
                     "ORDER BY a.created_at DESC\n"
                     "LIMIT 200")],
                campos(unit="short",
                       custom={"align": "auto", "cellOptions": {"type": "auto"},
                               "inspect": False, "filterable": True}),
                OPT_TABLA,
                "La ventana es el intervalo de eventos que sustenta la alerta. El identificador de correlacion permite reconstruir el incidente completo."))

p2.append(panel("geomap", "Ubicacion exacta de cada alerta", 0, 35, 24, 14, PG,
                [sql("SELECT a.created_at AS \"Hora\",\n"
                     "       a.type       AS \"Tipo\",\n"
                     "       a.zone       AS \"Zona\",\n"
                     "       round(a.score, 2) AS \"Confianza\",\n"
                     "       jsonb_array_length(coalesce(a.evidence, '[]'::jsonb))"
                     " AS \"Evidencias\",\n"
                     "       u.lat AS latitude,\n"
                     "       u.lon AS longitude\n"
                     "FROM alerts a\n"
                     + UBICACION + "\n"
                     "WHERE " + FILTRO_AL_A + " AND u.lat IS NOT NULL\n"
                     "ORDER BY a.created_at DESC\n"
                     "LIMIT 500")],
                campos(unit="short", decimals=2, mode="continuous-GrYlRd",
                       min_=0.8, max_=1.0),
                {"view": {"id": "coords", "lat": 14.6349, "lon": -90.5069, "zoom": 11,
                          "allLayers": True},
                 "basemap": {"type": "osm-standard", "name": "OpenStreetMap", "config": {}},
                 "layers": [
                     {"type": "markers", "name": "Alertas",
                      "tooltip": True, "opacity": 0.85,
                      "location": {"mode": "coords", "latitude": "latitude",
                                   "longitude": "longitude"},
                      "config": {"showLegend": True,
                                 "style": {"color": {"field": "Confianza", "fixed": "dark-red"},
                                           "opacity": 0.8,
                                           "size": {"field": "Evidencias", "fixed": 6,
                                                    "min": 5, "max": 14},
                                           "symbol": {"mode": "fixed",
                                                      "fixed": "img/icons/marker/circle.svg"},
                                           "symbolAlign": {"horizontal": "center",
                                                           "vertical": "center"},
                                           "textConfig": {"fontSize": 12, "offsetX": 0,
                                                          "offsetY": 0, "textAlign": "center",
                                                          "textBaseline": "middle"}}}}],
                 "controls": {"showZoom": True, "mouseWheelZoom": True,
                              "showAttribution": True, "showScale": False,
                              "showMeasure": True, "showDebug": False},
                 # "details" vuelca TODAS las columnas de la consulta al pasar
                 # el raton: hora, tipo, zona, confianza, evidencias y las dos
                 # coordenadas. Es lo que convierte un punto en un incidente
                 # identificable, en vez de una mancha de color.
                 "tooltip": {"mode": "details"}},
                "Un punto por alerta, situado en el promedio de las coordenadas de los eventos que la sustentan. El color es la confianza y el tamano el numero de evidencias. La regla de medicion permite estimar distancias entre incidentes."))

d2 = tablero("ciudad-incidentes", "Ciudad Inteligente — Incidentes y mapa de calor",
             "Mapa de calor, lista de incidentes y alertas criticas, sobre los datos archivados en PostgreSQL. Cubre los tres casos de visualizacion de la seccion 7 del enunciado.",
             ["ciudad-inteligente", "incidentes"], p2, [var_pg_zona()],
             refresco="30s", desde="now-24h")

# ===========================================================================
# 3. SLO y presupuesto de error  (Prometheus)
# ===========================================================================
_id[0] = 200
p3 = []

CUMPLIMIENTO = ('sum(increase(correlator_correlation_latency_seconds_bucket{le=~"2(\\\\.0)?"}[30d]))\n'
                '/\n'
                'sum(increase(correlator_correlation_latency_seconds_count[30d]))')

PRESUPUESTO = ('(\n'
               '  (\n'
               '    sum(increase(correlator_correlation_latency_seconds_count[30d]))\n'
               '    -\n'
               '    sum(increase(correlator_correlation_latency_seconds_bucket{le=~"2(\\\\.0)?"}[30d]))\n'
               '  )\n'
               '  /\n'
               '  sum(increase(correlator_correlation_latency_seconds_count[30d]))\n'
               ') / 0.01')

p3.append(panel("stat", "Cumplimiento del SLO de deteccion (30 dias)", 0, 0, 8, 5, PROM,
                [prom(CUMPLIMIENTO)],
                campos(unit="percentunit", decimals=4,
                       thr=umbrales((ROJO, None), (AMARILLO, 0.985), (VERDE, 0.99))),
                OPT_STAT,
                "Objetivo: el 99% de las alertas generadas en menos de 2 s desde el timestamp del evento disparador."))

p3.append(panel("gauge", "Presupuesto de error consumido", 8, 0, 8, 5, PROM,
                [prom(PRESUPUESTO)],
                campos(unit="percentunit", decimals=1, min_=0, max_=1,
                       thr=umbrales((VERDE, None), (AMARILLO, 0.5), (ROJO, 1))),
                {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                 "showThresholdLabels": False, "showThresholdMarkers": True,
                 "minVizWidth": 75, "minVizHeight": 75, "sizing": "auto"},
                "Por debajo del 50% se despliega con normalidad. Entre 50% y 100% se congelan los cambios no esenciales. Por encima del 100% se escala a coordinacion. (ADR-008)"))

p3.append(panel("stat", "Latencia media de deteccion (1 h)", 16, 0, 8, 5, PROM,
                [prom('sum(rate(correlator_correlation_latency_seconds_sum[1h]))\n'
                      '/\n'
                      'sum(rate(correlator_correlation_latency_seconds_count[1h]))')],
                campos(unit="s", decimals=4,
                       thr=umbrales((VERDE, None), (AMARILLO, 1), (ROJO, 2))),
                OPT_STAT, "Medicion de referencia del proyecto: 44 ms."))

p3.append(panel("timeseries", "Latencia de deteccion: p50, p95 y p99", 0, 5, 24, 9, PROM,
                [prom('histogram_quantile(0.50, sum(rate(correlator_correlation_latency_seconds_bucket[$__rate_interval])) by (le))', "p50", "A"),
                 prom('histogram_quantile(0.95, sum(rate(correlator_correlation_latency_seconds_bucket[$__rate_interval])) by (le))', "p95", "B"),
                 prom('histogram_quantile(0.99, sum(rate(correlator_correlation_latency_seconds_bucket[$__rate_interval])) by (le))', "p99", "C")],
                campos(unit="s", decimals=3, mode="palette-classic",
                       thr=umbrales((VERDE, None), (ROJO, 2)),
                       custom=dict(LINEA, fillOpacity=0,
                                   thresholdsStyle={"mode": "line"})),
                OPT_SERIE,
                "La linea roja en 2 s es el umbral del SLO. LatenciaDeteccionDegradandose avisa cuando el p95 supera 1 s, es decir, la mitad del margen."))

p3.append(panel("bargauge", "Cumplimiento por tipo de alerta (30 dias)", 0, 14, 12, 8, PROM,
                [prom('sum by (type) (increase(correlator_correlation_latency_seconds_bucket{le=~"2(\\\\.0)?"}[30d]))\n'
                      '/\n'
                      'sum by (type) (increase(correlator_correlation_latency_seconds_count[30d]))',
                      "{{type}}")],
                campos(unit="percentunit", decimals=3, min_=0.9, max_=1,
                       thr=umbrales((ROJO, None), (AMARILLO, 0.985), (VERDE, 0.99))),
                barra(),
                "Un tipo de alerta que incumpla en solitario apunta a su regla, no al sistema."))

p3.append(panel("timeseries", "Alertas emitidas frente a falsos positivos", 12, 14, 12, 8, PROM,
                [prom('sum(rate(correlator_alerts_generated_total[$__rate_interval])) * 60', "alertas emitidas/min", "A"),
                 prom('sum(rate(correlator_false_positive_candidates_total[$__rate_interval])) * 60', "candidatos a falso positivo/min", "B"),
                 prom('sum(rate(correlator_alerts_suppressed_total[$__rate_interval])) * 60', "suprimidas/min", "C")],
                campos(unit="short", decimals=2, mode="palette-classic", custom=LINEA),
                OPT_SERIE,
                "TasaAltaDeFalsosPositivos avisa si mas de la mitad de las coincidencias parciales expira sin completarse."))

p3.append(panel("bargauge", "Tareas nocturnas: tiempo desde el ultimo exito", 0, 22, 12, 7, PROM,
                [prom('time() - jobs_ultimo_exito_timestamp_seconds', "{{tarea}}")],
                campos(unit="s", decimals=0, min_=0,
                       thr=umbrales((VERDE, None), (AMARILLO, 97200), (ROJO, 129600))),
                barra(),
                "Se vigila por ausencia: si una tarea deja de ejecutarse no emite nada, asi que lo que alerta es que este contador siga creciendo. Rojo a las 36 h."))

p3.append(panel("stat", "Integridad del flujo (24 h)", 12, 22, 12, 7, PROM,
                [prom('sum(increase(ingestor_events_ingested_total[24h]))', "aceptados con 202", "A"),
                 prom('sum(increase(persister_eventos_persistidos_total[24h]))', "archivados en PostgreSQL", "B"),
                 prom('sum(increase(indexer_documentos_indexados_total{flujo="eventos"}[24h]))', "indexados en Elasticsearch", "C")],
                campos(unit="short", decimals=0, mode="palette-classic"),
                dict(OPT_STAT, textMode="value_and_name", graphMode="none",
                     colorMode="value", orientation="horizontal"),
                "Los tres numeros deben coincidir. Una divergencia sostenida significa perdida de eventos; una transitoria, solo retraso del archivo."))

d3 = tablero("ciudad-slo", "Ciudad Inteligente — SLO y presupuesto de error",
             "Cumplimiento de los objetivos de nivel de servicio del ADR-008 y consumo del presupuesto de error.",
             ["ciudad-inteligente", "slo"], p3, [],
             refresco="1m", desde="now-6h")

# ===========================================================================
# 4. Trazabilidad: a quien se aviso y que evento se encadena con cual
# ===========================================================================

_id[0] = 300
p4 = []

# Las notificaciones heredan la zona de su alerta, pero puede ser nula si la
# alerta no la traia: se incluyen igualmente para que un aviso nunca
# desaparezca del tablero por un dato que falta aguas arriba.
FILTRO_NOTIF = "$__timeFilter(n.creado_en) AND (n.zone LIKE '$zona' OR n.zone IS NULL)"


def var_incidente():
    # Un incidente se identifica por el boton de panico que lo arranca.
    #
    # La consulta devuelve __text y __value: Grafana muestra lo primero en el
    # selector y manda lo segundo a las consultas. Sin esa separacion el
    # desplegable mostraria UUID crudos, que nadie puede reconocer, y habria
    # que elegir entre una etiqueta legible o un identificador utilizable.
    sql = ("SELECT to_char(e.ts_utc, 'DD/MM HH24:MI:SS') || '  ' ||\n"
           "       coalesce(e.zone, 'sin zona') AS __text,\n"
           "       e.event_id::text AS __value\n"
           "FROM events e\n"
           "WHERE e.event_type = 'panic.button'\n"
           "  AND e.ts_utc > now() - interval '24 hours'\n"
           "ORDER BY e.ts_utc DESC\n"
           "LIMIT 100")
    return {
        "name": "incidente", "label": "Incidente", "type": "query",
        "datasource": PG, "definition": sql,
        "query": {"refId": "tpl-incidente", "rawSql": sql, "format": "table",
                  "editorMode": "code", "rawQuery": True},
        # Sin "All": la trazabilidad de todos los incidentes a la vez es una
        # maranha de lineas cruzadas que no dice nada. Se mira de uno en uno.
        "refresh": 1, "includeAll": False, "multi": False,
        "sort": 0, "options": [],
        "current": {},
    }



# --------------------------------------------- trazabilidad en el mapa ----

def capa_calles(refid="B", nombre="Recorrido por calles"):
    # Dibuja la geometria que calculo el motor de rutas: cientos de vertices
    # que siguen el trazado real de las calles. La capa sigue siendo de tipo
    # "route" —une puntos consecutivos con segmentos rectos— pero con los
    # vertices tan juntos el resultado es indistinguible de una ruta de
    # navegador.
    #
    # filterData ata la capa a una consulta concreta. Sin eso, Grafana le
    # pasaria a cada capa la primera trama que encuentre y la ruta intentaria
    # dibujarse con los puntos de los avistamientos.
    return {
        "type": "route", "name": nombre,
        "tooltip": False, "opacity": 1,
        "filterData": {"id": "byRefId", "options": refid},
        "location": {"mode": "coords", "latitude": "latitude",
                     "longitude": "longitude"},
        "config": {"style": {"color": {"fixed": "dark-blue"},
                             "opacity": 0.9,
                             "lineWidth": 5,
                             "size": {"fixed": 5, "min": 1, "max": 8},
                             "arrow": 1}},
    }


def capa_marcas(campo_color="Orden", nombre="Apariciones", refid="A", rotulo=None):
    # Las marcas se colorean por el orden de aparicion: la primera sale verde
    # y la ultima roja.
    #
    # El degradado por si solo no basta. Con cuatro puntos se distingue; con
    # doce, o con el mapa alejado, nadie sabe cual fue el primero y cual el
    # ultimo. Por eso los extremos llevan ademas una palabra escrita encima,
    # INICIO y FIN: un color es una pista, una palabra es un dato.
    return {
        "type": "markers", "name": nombre,
        "tooltip": True, "opacity": 0.95,
        "filterData": {"id": "byRefId", "options": refid},
        "location": {"mode": "coords", "latitude": "latitude",
                     "longitude": "longitude"},
        "config": {"showLegend": False,
                   "style": {"color": {"field": campo_color, "fixed": "dark-red"},
                             "opacity": 0.9,
                             "text": ({"mode": "field", "field": rotulo, "fixed": ""}
                                      if rotulo else {"mode": "fixed", "fixed": ""}),
                             "size": {"fixed": 9, "min": 7, "max": 14},
                             "symbol": {"mode": "fixed",
                                        "fixed": "img/icons/marker/circle.svg"},
                             "symbolAlign": {"horizontal": "center",
                                             "vertical": "center"},
                             "textConfig": {"fontSize": 13, "offsetX": 0,
                                            "offsetY": 18, "textAlign": "center",
                                            "textBaseline": "middle"}}},
    }


def opciones_mapa(capas):
    return {
        "view": {"id": "coords", "lat": 14.6349, "lon": -90.5069, "zoom": 11,
                 "allLayers": True},
        "basemap": {"type": "osm-standard", "name": "OpenStreetMap", "config": {}},
        "layers": capas,
        "controls": {"showZoom": True, "mouseWheelZoom": True,
                     "showAttribution": True, "showScale": False,
                     "showMeasure": True, "showDebug": False},
        "tooltip": {"mode": "details"},
    }


# El rastro completo de un incidente, de principio a ultimo contacto.
#
# Tiene dos partes y la distincion importa. La EVIDENCIA son los eventos que
# sustentan las alertas correlacionadas a partir de ese boton de panico. El
# SEGUIMIENTO son las apariciones posteriores del mismo vehiculo.
#
# El seguimiento NO se corta a los pocos minutos. La ventana de correlacion es
# de DETECCION —tiene que ser corta, una alerta de robo que llega media hora
# tarde no sirve— pero reconstruir el incidente es otra cosa, se hace sobre el
# archivo y no tiene por que heredar ese limite. Lo que interesa saber es por
# donde paso el vehiculo y DONDE SE LE PERDIO LA PISTA, que es justo la
# informacion que convierte una alerta en una investigacion.
#
# El rastro termina cuando entre dos apariciones pasa mas de media hora. Es una
# politica, no una medida: si un coche reaparece pasados treinta minutos, eso ya
# no es la misma huida sino otra aparicion. El valor se deja a la vista aqui
# para que se pueda discutir.
#
# Lo que el sistema NO puede decir es POR QUE se perdio el contacto: si el
# vehiculo se detuvo, salio de la ciudad, o sencillamente dejo de haber camaras.
# Distinguirlo exigiria un inventario de cobertura de sensores que el sistema no
# tiene. Conviene no sugerir lo contrario al presentarlo.
RASTRO = (
                     "WITH cadena AS (\n"
                     "  SELECT DISTINCT ev.id::uuid AS eid\n"
                     "  FROM alerts a\n"
                     "  CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)\n"
                     "  WHERE jsonb_exists(a.evidence, '$incidente')\n"
                     "),\n"
                     "arranque AS (\n"
                     "  SELECT min(e.ts_utc) AS t0\n"
                     "  FROM events e JOIN cadena c ON e.event_id = c.eid\n"
                     "),\n"
                     "vehiculos AS (\n"
                     "  SELECT DISTINCT e.payload->>'placa_vehicular' AS placa\n"
                     "  FROM events e JOIN cadena c ON e.event_id = c.eid\n"
                     "  WHERE e.payload ? 'placa_vehicular'\n"
                     "),\n"
                     "bruto AS (\n"
                     "  SELECT e.event_id,\n"
                     "         e.payload->>'placa_vehicular' AS placa,\n"
                     "         lag(e.ts_utc) OVER (PARTITION BY e.payload->>'placa_vehicular'\n"
                     "                             ORDER BY e.ts_utc) AS anterior,\n"
                     "         e.ts_utc\n"
                     "  FROM events e\n"
                     "  WHERE e.payload->>'placa_vehicular' IN (SELECT placa FROM vehiculos)\n"
                     "    AND e.ts_utc >= (SELECT t0 FROM arranque)\n"
                     "),\n"
                     "rastro AS (\n"
                     "  SELECT event_id,\n"
                     "         sum(CASE WHEN anterior IS NULL\n"
                     "                    OR ts_utc - anterior <= interval '30 minutes'\n"
                     "                  THEN 0 ELSE 1 END)\n"
                     "           OVER (PARTITION BY placa ORDER BY ts_utc) AS tramo\n"
                     "  FROM bruto\n"
                     "),\n"
                     "incidente AS (\n"
                     "  SELECT c.eid AS event_id, true AS es_evidencia FROM cadena c\n"
                     "  UNION\n"
                     "  SELECT r.event_id, false\n"
                     "  FROM rastro r\n"
                     "  WHERE r.tramo = 0\n"
                     "    AND NOT EXISTS (SELECT 1 FROM cadena c WHERE c.eid = r.event_id)\n"
                     ")\n"
)

RASTRO_WHERE = (
                     "FROM incidente i\n"
                     "JOIN events e ON e.event_id = i.event_id\n"
                     "WHERE e.geo_lat IS NOT NULL\n"
                     "ORDER BY e.ts_utc\n"
)

# Geometria del recorrido por calles, calculada por el motor de rutas y
# guardada por la tarea periodica de jobs.
#
# De todas las alertas del incidente se toma la de cadena mas larga: es la que
# recorre mas puntos y, por tanto, la que describe el incidente completo. Unir
# las geometrias de varias alertas produciria una linea que salta de un extremo
# a otro cada vez que empieza la siguiente.
GEOMETRIA = (
    "WITH alerta AS (\n"
    "  SELECT a.alert_id\n"
    "  FROM alerts a\n"
    "  WHERE jsonb_exists(a.evidence, '$incidente')\n"
    "  ORDER BY jsonb_array_length(a.evidence) DESC, a.created_at DESC\n"
    "  LIMIT 1\n"
    ")\n"
    "SELECT (p->>1)::float8 AS latitude,\n"
    "       (p->>0)::float8 AS longitude,\n"
    "       ord            AS \"Vertice\"\n"
    "FROM alert_routes r\n"
    "JOIN alerta x ON x.alert_id = r.alert_id\n"
    "CROSS JOIN LATERAL jsonb_array_elements(r.geometria)\n"
    "  WITH ORDINALITY AS t(p, ord)\n"
    "WHERE r.estado = 'calculada'\n"
    "ORDER BY ord")

# Al pulsar CUALQUIER celda de una fila, el tablero se recarga con esa fila
# como incidente seleccionado y el mapa dibuja su trazabilidad. Pulsar otra
# fila muestra la otra: es la forma de recorrer muchos pulsadores sin tener que
# buscarlos en el desplegable, que es como se va a usar cuando no haya uno sino
# decenas.
#
# Se usa ${zona:queryparam} y no "var-zona=${zona}". La diferencia aparece con
# el filtro en "All": la segunda forma manda el comodin "%" como valor literal
# y el selector deja de decir "All" para decir "%". La sintaxis queryparam
# genera ella misma el parametro y conserva la seleccion especial.
ENLACE_INCIDENTE = [{
    "title": "Ver la trazabilidad de este incidente en el mapa",
    "url": "/d/ciudad-trazabilidad?${__url_time_range}&${zona:queryparam}"
           "&var-incidente=${__data.fields.Incidente}",
    "targetBlank": False,
}]

# --------------------------------------------------------------- avisos ----

p4.append(panel("stat", "Avisos emitidos", 0, 0, 6, 4, PG,
                [sql("SELECT count(*) AS avisos FROM notifications n WHERE " + FILTRO_NOTIF)],
                campos(unit="short", decimals=0), OPT_STAT,
                "Un aviso es una fila por organismo: una misma alerta de incendio produce dos, uno a bomberos y otro a ambulancia."))

p4.append(panel("stat", "Organismos avisados", 6, 0, 6, 4, PG,
                [sql("SELECT count(DISTINCT n.organismo) AS organismos\n"
                     "FROM notifications n WHERE " + FILTRO_NOTIF)],
                campos(unit="short", decimals=0), OPT_STAT))

p4.append(panel("stat", "Avisos fallidos", 12, 0, 6, 4, PG,
                [sql("SELECT count(*) FILTER (WHERE n.estado = 'fallido') AS fallidos\n"
                     "FROM notifications n WHERE " + FILTRO_NOTIF)],
                campos(unit="short", decimals=0,
                       thr=umbrales((VERDE, None), (ROJO, 1))),
                dict(OPT_STAT, colorMode="background"),
                "Un aviso fallido queda registrado igual que uno entregado: la diferencia entre 'no se pudo avisar' y 'no se intento' es justo lo que necesita saber una investigacion posterior."))

p4.append(panel("stat", "Alertas sin ningun aviso", 18, 0, 6, 4, PG,
                [sql("SELECT count(*) AS sin_aviso\n"
                     "FROM alerts a\n"
                     "WHERE " + FILTRO_AL_A + "\n"
                     "  AND NOT EXISTS (\n"
                     "        SELECT 1 FROM notifications n WHERE n.alert_id = a.alert_id)")],
                campos(unit="short", decimals=0,
                       thr=umbrales((VERDE, None), (ROJO, 1))),
                dict(OPT_STAT, colorMode="background"),
                "El numero que de verdad importa. Una alerta sin aviso no produce ningun error: el sistema sigue funcionando y sencillamente nadie acude. Deberia ser cero; si no lo es, falta una fila en la matriz de despacho o el notificador estuvo caido."))

p4.append(panel("table", "Avisos a terceros", 0, 4, 14, 10, PG,
                [sql("SELECT n.creado_en   AS \"Hora\",\n"
                     "       n.alert_type  AS \"Tipo de alerta\",\n"
                     "       n.zone        AS \"Zona\",\n"
                     "       n.organismo   AS \"Organismo\",\n"
                     "       n.canal       AS \"Canal\",\n"
                     "       n.estado      AS \"Estado\",\n"
                     "       coalesce(n.detalle, '') AS \"Detalle\"\n"
                     "FROM notifications n\n"
                     "WHERE " + FILTRO_NOTIF + "\n"
                     "ORDER BY n.creado_en DESC\n"
                     "LIMIT 300")],
                campos(unit="short",
                       custom={"align": "auto", "cellOptions": {"type": "auto"},
                               "inspect": False, "filterable": True}),
                OPT_TABLA,
                "Canal 'simulado' significa que el aviso quedo registrado pero NO se entrego a ningun organismo real: no existe integracion con bomberos, policia ni ambulancias."))

p4.append(panel("barchart", "Avisos por organismo", 14, 4, 10, 5, PG,
                [sql("SELECT n.organismo AS \"Organismo\", count(*) AS \"Avisos\"\n"
                     "FROM notifications n WHERE " + FILTRO_NOTIF + "\n"
                     "GROUP BY 1 ORDER BY 2 DESC")],
                campos(unit="short", decimals=0, mode="palette-classic",
                       custom={"lineWidth": 1, "fillOpacity": 80, "axisPlacement": "auto",
                               "gradientMode": "none", "thresholdsStyle": {"mode": "off"}}),
                {"orientation": "horizontal", "xTickLabelRotation": 0,
                 "xTickLabelSpacing": 0, "showValue": "auto", "stacking": "none",
                 "groupWidth": 0.7, "barWidth": 0.8, "barRadius": 0,
                 "fullHighlight": False,
                 "tooltip": {"mode": "single", "sort": "none"},
                 "legend": {"displayMode": "list", "placement": "bottom", "showLegend": False}}))

p4.append(panel("piechart", "Reparto por tipo de alerta", 14, 9, 10, 5, PG,
                [sql("SELECT n.alert_type AS \"Tipo\", count(*) AS \"Avisos\"\n"
                     "FROM notifications n WHERE " + FILTRO_NOTIF + "\n"
                     "GROUP BY 1 ORDER BY 2 DESC")],
                campos(unit="short", decimals=0, mode="palette-classic"),
                {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": True},
                 "pieType": "donut", "displayLabels": ["percent"],
                 "tooltip": {"mode": "single", "sort": "none"},
                 "legend": {"displayMode": "list", "placement": "right", "showLegend": True}}))

# ------------------------------------------------- cadena de un incidente ---

# El grafo necesita DOS consultas con nombres de campo exactos: la de nodos
# debe traer "id", y la de aristas "id", "source" y "target". Si un nombre no
# coincide, el panel no da error: aparece vacio.
#
# Se limita a las alertas mas recientes porque un grafo con cientos de nodos no
# se lee: deja de ser una explicacion y pasa a ser una mancha.
RECIENTES = (
    "WITH recientes AS (\n"
    "  SELECT a.alert_id, a.type, a.zone, a.score, a.evidence, a.created_at\n"
    "  FROM alerts a\n"
    "  WHERE " + FILTRO_AL_A + "\n"
    "  ORDER BY a.created_at DESC\n"
    "  LIMIT 12\n"
    ")\n")

p4.append(panel("nodeGraph", "Que evento se correlaciona con cual", 0, 14, 24, 13, PG,
                [sql(RECIENTES +
                     "SELECT r.alert_id::text AS id,\n"
                     "       r.type           AS title,\n"
                     "       r.zone           AS \"subTitle\",\n"
                     "       round(r.score, 2)::text AS \"mainStat\",\n"
                     "       'ALERTA'         AS \"secondaryStat\"\n"
                     "FROM recientes r\n"
                     "UNION\n"
                     "SELECT DISTINCT e.event_id::text,\n"
                     "       e.event_type,\n"
                     "       e.zone,\n"
                     "       coalesce(e.payload->>'infraccion',\n"
                     "                e.payload->>'tipo_evento',\n"
                     "                e.payload->>'tipo_sonido_detectado',\n"
                     "                e.payload->>'velocidad_estimada',\n"
                     "                '') ,\n"
                     "       'evidencia'\n"
                     "FROM recientes r\n"
                     "CROSS JOIN LATERAL jsonb_array_elements_text(r.evidence) AS ev(id)\n"
                     "JOIN events e ON e.event_id = ev.id::uuid", "A"),
                 sql(RECIENTES +
                     "SELECT r.alert_id::text || ':' || ev.id AS id,\n"
                     "       r.alert_id::text AS source,\n"
                     "       ev.id            AS target\n"
                     "FROM recientes r\n"
                     "CROSS JOIN LATERAL jsonb_array_elements_text(r.evidence) AS ev(id)", "B")],
                campos(),
                {"nodes": {"mainStatUnit": "", "secondaryStatUnit": ""},
                 "edges": {"mainStatUnit": ""}},
                "Cada alerta con los eventos que la sustentan. La relacion sale de alerts.evidence, no de correlation_id: el enriquecedor asigna un correlation_id distinto a cada evento, de modo que ese campo no agrupa nada."))

p4.append(panel("table", "Evidencias, evento por evento", 0, 27, 24, 10, PG,
                [sql(RECIENTES +
                     "SELECT r.created_at  AS \"Alerta\",\n"
                     "       r.type        AS \"Tipo de alerta\",\n"
                     "       e.ts_utc      AS \"Hora del evento\",\n"
                     "       e.event_type  AS \"Sensor\",\n"
                     "       e.zone        AS \"Zona\",\n"
                     "       coalesce(e.payload->>'infraccion', '') AS \"Infraccion\",\n"
                     "       coalesce(e.payload->>'velocidad_estimada',\n"
                     "                e.payload->>'velocidad_detectada', '') AS \"Velocidad\",\n"
                     "       left(coalesce(e.payload->>'placa_vehicular', ''), 14) AS \"Vehiculo\"\n"
                     "FROM recientes r\n"
                     "CROSS JOIN LATERAL jsonb_array_elements_text(r.evidence) AS ev(id)\n"
                     "JOIN events e ON e.event_id = ev.id::uuid\n"
                     "ORDER BY r.created_at DESC, e.ts_utc")],
                campos(unit="short",
                       custom={"align": "auto", "cellOptions": {"type": "auto"},
                               "inspect": False, "filterable": True}),
                OPT_TABLA,
                "La misma informacion que el grafo, en forma de lista. Se mantiene a proposito: si el grafo no renderiza, esta tabla sigue respondiendo la pregunta."))

fc_incidentes = campos(unit="short",
                       custom={"align": "auto", "cellOptions": {"type": "auto"},
                               "inspect": False, "filterable": True})
fc_incidentes["defaults"]["links"] = ENLACE_INCIDENTE

p4.append(panel("table", "Incidentes: botones de panico", 0, 37, 11, 12, PG,
                [sql("SELECT e.ts_utc AS \"Hora\",\n"
                     "       e.zone   AS \"Zona\",\n"
                     "       coalesce(e.payload->>'tipo_de_alerta', '') AS \"Aviso\",\n"
                     "       coalesce((SELECT string_agg(DISTINCT a.type, ', ')\n"
                     "                 FROM alerts a\n"
                     "                 WHERE jsonb_exists(a.evidence, e.event_id::text)),\n"
                     "                'sin correlacionar') AS \"Derivo en\",\n"
                     "       e.event_id::text AS \"Incidente\"\n"
                     "FROM events e\n"
                     "WHERE e.event_type = 'panic.button'\n"
                     "  AND $__timeFilter(e.ts_utc)\n"
                     "  AND e.zone LIKE '$zona'\n"
                     "ORDER BY e.ts_utc DESC\n"
                     "LIMIT 200")],
                fc_incidentes, OPT_TABLA,
                "Solo botones de panico: cada fila es el arranque de un incidente. Al pulsar una fila el mapa de al lado dibuja su trazabilidad completa. La columna 'Derivo en' dice que alertas se correlacionaron a partir de ese boton; 'sin correlacionar' significa que el pulsador no encontro eventos que lo acompanaran, que es el caso de falso positivo del enunciado."))

p4.append(panel("geomap", "Trazabilidad del incidente, de principio a fin", 11, 37, 13, 12, PG,
                [sql(RASTRO +
                     "SELECT e.geo_lat AS latitude,\n"
                     "       e.geo_lon AS longitude,\n"
                     "       row_number() OVER (ORDER BY e.ts_utc) AS \"Orden\",\n"
                     "       CASE\n"
                     "         WHEN row_number() OVER (ORDER BY e.ts_utc) = 1\n"
                     "           THEN 'INICIO'\n"
                     "         WHEN row_number() OVER (ORDER BY e.ts_utc DESC) = 1\n"
                     "           THEN 'ULTIMO CONTACTO'\n"
                     "         ELSE ''\n"
                     "       END          AS \"Hito\",\n"
                     "       e.ts_utc     AS \"Hora\",\n"
                     "       e.zone       AS \"Zona\",\n"
                     "       e.event_type AS \"Sensor\",\n"
                     "       CASE WHEN i.es_evidencia THEN 'evidencia'\n"
                     "            ELSE 'seguimiento' END AS \"Origen\",\n"
                     "       coalesce(e.payload->>'infraccion', '') AS \"Infraccion\",\n"
                     "       coalesce(e.payload->>'velocidad_estimada',\n"
                     "                e.payload->>'velocidad_detectada', '') AS \"Velocidad\",\n"
                     "       left(coalesce(e.payload->>'placa_vehicular', ''), 14) AS \"Vehiculo\"\n"
                     + RASTRO_WHERE, "A"),
                 sql(GEOMETRIA, "B")],
                campos(unit="short", decimals=0, mode="continuous-GrYlRd"),
                opciones_mapa([capa_calles("B", "Recorrido por calles"),
                               capa_marcas("Orden", "Donde lo vieron las camaras",
                                           "A", rotulo="Hito")]),
                "El punto verde rotulado INICIO es donde empezo el incidente; el rojo rotulado ULTIMO CONTACTO es la ultima vez que un sensor vio el vehiculo. La linea azul es el recorrido por calles que el motor de rutas considera mas probable entre esos puntos, y la flecha marca el sentido. OJO al explicarlo: el sistema NO midio ese camino, midio los puntos. La columna Origen distingue 'evidencia' (lo que sostiene la alerta) de 'seguimiento' (apariciones posteriores del mismo vehiculo). El rastro se corta cuando pasan mas de 30 minutos sin verlo; el sistema NO puede saber si eso fue porque se detuvo, porque salio de la ciudad o porque dejo de haber camaras. Pulse una fila de la tabla de la izquierda para ver otro incidente."))

d4 = tablero("ciudad-trazabilidad", "Ciudad Inteligente — Trazabilidad de avisos y cadenas",
             "A que organismos se aviso de cada alerta, que eventos se correlacionan entre si, y por donde paso el vehiculo implicado.",
             ["ciudad-inteligente", "trazabilidad"], p4,
             [var_pg_zona(), var_incidente()],
             refresco="30s", desde="now-24h")

# ===========================================================================
for nombre, d in (("01-operacion-tiempo-real.json", d1),
                  ("02-incidentes-y-mapa-de-calor.json", d2),
                  ("03-slo-y-presupuesto.json", d3),
                  ("04-trazabilidad.json", d4)):
    ruta = os.path.join(DESTINO, nombre)
    io.open(ruta, "w", encoding="utf-8").write(
        json.dumps(d, indent=2, ensure_ascii=False) + "\n")
    print("  %-38s %2d paneles" % (nombre, len(d["panels"])))
