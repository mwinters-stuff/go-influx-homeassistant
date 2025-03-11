# secrets

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: influxdb-mqtt-homeassistant-secrets
  namespace: apps
type: Opaque
stringData:
  INFLUX_TOKEN: "your-influxdb-token"
  MQTT_USERNAME: "your-mqtt-user"
  MQTT_PASSWORD: "your-mqtt-password"
```