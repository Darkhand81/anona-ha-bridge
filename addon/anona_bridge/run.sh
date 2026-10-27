#!/usr/bin/with-contenv bashio
# Translate the add-on options into the ANONA_* env the bridge reads, then exec
# it. MQTT credentials come from the add-on options, or (when mqtt_url is blank)
# automatically from a running Mosquitto add-on via the Supervisor services API.
set -e

export ANONA_EMAIL="$(bashio::config 'email')"
export ANONA_PASSWORD="$(bashio::config 'password')"
export ANONA_REGION="$(bashio::config 'region')"
export ANONA_RTSP_LISTEN="$(bashio::config 'rtsp_listen')"
export ANONA_HTTP_LISTEN="$(bashio::config 'http_listen')"
export ANONA_TOKEN_REFRESH="$(bashio::config 'token_refresh')"

# Optional RTSP auth: leave blank to keep the stream open.
if bashio::config.has_value 'rtsp_user'; then
    export ANONA_RTSP_USER="$(bashio::config 'rtsp_user')"
    export ANONA_RTSP_PASS="$(bashio::config 'rtsp_password')"
fi

if bashio::config.true 'has_battery'; then
    export ANONA_HAS_BATTERY=1
fi

# Optional deviceId allowlist -> comma-separated.
if bashio::config.has_value 'devices'; then
    export ANONA_DEVICES="$(bashio::config 'devices | join(",")')"
fi

# MQTT: explicit option wins; otherwise auto-discover the Mosquitto add-on.
if bashio::config.has_value 'mqtt_url'; then
    export ANONA_MQTT_URL="$(bashio::config 'mqtt_url')"
    export ANONA_MQTT_USER="$(bashio::config 'mqtt_user')"
    export ANONA_MQTT_PASS="$(bashio::config 'mqtt_password')"
    bashio::log.info "Using configured MQTT broker ${ANONA_MQTT_URL}"
elif bashio::services.available 'mqtt'; then
    export ANONA_MQTT_URL="tcp://$(bashio::services 'mqtt' 'host'):$(bashio::services 'mqtt' 'port')"
    export ANONA_MQTT_USER="$(bashio::services 'mqtt' 'username')"
    export ANONA_MQTT_PASS="$(bashio::services 'mqtt' 'password')"
    bashio::log.info "Using Home Assistant MQTT service at ${ANONA_MQTT_URL}"
else
    bashio::log.warning "No MQTT broker configured or available; running without HA entities"
fi

if bashio::var.is_empty "${ANONA_EMAIL}"; then
    bashio::exit.nok "Set the Anona account 'email' and 'password' in the add-on options"
fi

bashio::log.info "Starting Anona Pano Bridge (region ${ANONA_REGION})"
exec anona-bridge
