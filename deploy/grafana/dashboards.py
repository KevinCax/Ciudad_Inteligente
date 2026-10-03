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
for nombre, d in (("01-operacion-tiempo-real.json", d1),
                  ("02-incidentes-y-mapa-de-calor.json", d2),
                  ("03-slo-y-presupuesto.json", d3)):
    ruta = os.path.join(DESTINO, nombre)
    io.open(ruta, "w", encoding="utf-8").write(
        json.dumps(d, indent=2, ensure_ascii=False) + "\n")
    print("  %-38s %2d paneles" % (nombre, len(d["panels"])))
