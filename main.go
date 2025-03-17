package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

// Load environment variables with default values
var (
	influxURL             = getEnv("INFLUX_URL", "http://localhost:8086")
	influxToken           = getEnv("INFLUX_TOKEN", "")
	influxOrg             = getEnv("INFLUX_ORG", "your-org")
	influxBucket          = getEnv("INFLUX_BUCKET", "your-bucket")
	mqttBroker            = getEnv("MQTT_BROKER", "tcp://homeassistant.local:1883")
	mqttUsername          = getEnv("MQTT_USERNAME", "")
	mqttPassword          = getEnv("MQTT_PASSWORD", "")
	mqttSensor            = getEnv("MQTT_SENSOR", "influx-import")
	publishInterval       = 2 * time.Minute // Send rain & wind data every 2 minutes
	configPublishInterval = 12 * time.Hour  // Republish MQTT discovery config every 12 hours

)

// Utility function to get environment variables with a fallback default value
func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// MQTT Configuration
const (
	mqttRainTopic  = "homeassistant/sensor/%s/rain/state"
	mqttRainConfig = "homeassistant/sensor/%s/rain/config"

	mqttWindTopic  = "homeassistant/sensor/%s/wind-max/state"
	mqttWindConfig = "homeassistant/sensor/%s/wind-max/config"

	mqttWindGustTopic  = "homeassistant/sensor/%s/wind-gust-max/state"
	mqttWindGustConfig = "homeassistant/sensor/%s/wind-gust-max/config"

	mqttMinTempTopic  = "homeassistant/sensor/%s/temperature-min/state"
	mqttMinTempConfig = "homeassistant/sensor/%s/temperature-min/config"
	mqttMaxTempTopic  = "homeassistant/sensor/%s/temperature-max/state"
	mqttMaxTempConfig = "homeassistant/sensor/%s/temperature-max/config"

	mqttMinHumidTopic  = "homeassistant/sensor/%s/humidity-min/state"
	mqttMinHumidConfig = "homeassistant/sensor/%s/humidity-min/config"
	mqttMaxHumidTopic  = "homeassistant/sensor/%s/humidity-max/state"
	mqttMaxHumidConfig = "homeassistant/sensor/%s/humidity-max/config"

	mqttMinPressureTopic  = "homeassistant/sensor/%s/pressure-min/state"
	mqttMinPressureConfig = "homeassistant/sensor/%s/pressure-min/config"
	mqttMaxPressureTopic  = "homeassistant/sensor/%s/pressure-max/state"
	mqttMaxPressureConfig = "homeassistant/sensor/%s/pressure-max/config"

	mqttAvail = "homeassistant/sensor/%s/availability"
)

// Retry Settings
const (
	maxRetries = 5
	retryDelay = 5 * time.Second
)

// Home Assistant MQTT Discovery Config
type MqttConfig struct {
	DeviceClass         string `json:"device_class"`
	Name                string `json:"name"`
	StateTopic          string `json:"state_topic"`
	StateClass          string `json:"state_class"`
	UnitOfMeasurement   string `json:"unit_of_measurement"`
	ValueTemplate       string `json:"value_template"`
	UniqueID            string `json:"unique_id"`
	AvailabilityTopic   string `json:"availability_topic"`
	PayloadAvailable    string `json:"payload_available"`
	PayloadNotAvailable string `json:"payload_not_available"`
	Device              Device `json:"device"`
}

type Device struct {
	Name          string `json:"name"`
	SuggestedArea string `json:"suggested_area"`
	Identifiers   string `json:"identifiers"` // Add Identifiers field
}

// Set up logging to file and console
func setupLogging() {
	// logFile, err := os.OpenFile("app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	// if err != nil {
	// 	log.Fatalf("Failed to open log file: %v", err)
	// }
	// log.SetOutput()
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

// Query InfluxDB for rain data since midnight
func queryInfluxDB(field, aggFunction string) (float64, error) {
	log.Printf("Querying InfluxDB for %s of %s data...\n", aggFunction, field)
	return queryInfluxDBValue("sensor-data", field, aggFunction)
}

// Generalized InfluxDB query function
func queryInfluxDBValue(measurement, field, aggFunction string) (float64, error) {
	client := influxdb2.NewClient(influxURL, influxToken)
	defer client.Close()

	queryAPI := client.QueryAPI(influxOrg)

	// Get timestamp of midnight
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	midnightStr := midnight.Format(time.RFC3339)

	log.Printf("Midnight timestamp: %s", midnightStr)

	query := fmt.Sprintf(`from(bucket: "%s") 
		|> range(start: %s) 
		|> filter(fn: (r) => r._measurement == "%s") 
		|> filter(fn: (r) => r._field == "%s") 
		|> %s()`, influxBucket, midnightStr, measurement, field, aggFunction)

	var value float64
	for i := 1; i <= maxRetries; i++ {
		result, err := queryAPI.Query(context.Background(), query)
		if err != nil {
			log.Printf("InfluxDB query failed (attempt %d/%d): %v", i, maxRetries, err)
			time.Sleep(retryDelay)
			continue
		}

		for result.Next() {
			if v, ok := result.Record().Value().(float64); ok {
				value = v
			}
		}

		if result.Err() != nil {
			log.Printf("InfluxDB result error: %v", result.Err())
			time.Sleep(retryDelay)
			continue
		}

		log.Printf("InfluxDB query successful: %s = %.2f", measurement, value)
		return value, nil
	}

	return 0, fmt.Errorf("failed to retrieve %s from InfluxDB after %d attempts", measurement, maxRetries)
}

func extractSensorType(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) > 3 {
		return parts[3]
	}
	return ""
}

func generateMqttConfig(device Device, stateTopic, deviceClass, name, unit, stateClass string) MqttConfig {
	return MqttConfig{
		DeviceClass:         deviceClass,
		Name:                name,
		StateTopic:          fmt.Sprintf(stateTopic, mqttSensor),
		StateClass:          stateClass,
		UnitOfMeasurement:   unit,
		ValueTemplate:       "{{ value | float }}",
		UniqueID:            fmt.Sprintf("%s-sensor-%s", mqttSensor, extractSensorType(stateTopic)),
		AvailabilityTopic:   fmt.Sprintf(mqttAvail, mqttSensor),
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
		Device:              device,
	}
}

// Publish MQTT Discovery Config for Home Assistant
func publishMqttConfig(client mqtt.Client) {
	log.Println("Publishing MQTT discovery config...")

	var device = Device{Name: "Influx Import", SuggestedArea: "Garage", Identifiers: mqttSensor}

	configs := []struct {
		Topic  string
		Config MqttConfig
	}{
		{
			fmt.Sprintf(mqttRainConfig, mqttSensor),
			generateMqttConfig(device, mqttRainTopic, "precipitation", "Rainfall Sensor", "mm", "total_increasing"),
		},
		{
			fmt.Sprintf(mqttWindConfig, mqttSensor),
			generateMqttConfig(device, mqttWindTopic, "wind_speed", "Max Wind Speed", "km/h", "measurement"),
		},
		{
			fmt.Sprintf(mqttWindGustConfig, mqttSensor),
			generateMqttConfig(device, mqttWindGustTopic, "wind_speed", "Max Wind Gust Speed", "km/h", "measurement"),
		},
		{
			fmt.Sprintf(mqttMinTempConfig, mqttSensor),
			generateMqttConfig(device, mqttMinTempTopic, "temperature", "Minimum Temperature", "℃", "measurement"),
		},
		{
			fmt.Sprintf(mqttMaxTempConfig, mqttSensor),
			generateMqttConfig(device, mqttMaxTempTopic, "temperature", "Maximum Temperature", "℃", "measurement"),
		},
		{
			fmt.Sprintf(mqttMinHumidConfig, mqttSensor),
			generateMqttConfig(device, mqttMinHumidTopic, "humidity", "Minimum Humidity", "%", "measurement"),
		},
		{
			fmt.Sprintf(mqttMaxHumidConfig, mqttSensor),
			generateMqttConfig(device, mqttMaxHumidTopic, "humidity", "Maximum Humidity", "%", "measurement"),
		},
		{
			fmt.Sprintf(mqttMinPressureConfig, mqttSensor),
			generateMqttConfig(device, mqttMinPressureTopic, "pressure", "Minimum Pressure", "hPa", "measurement"),
		},
		{
			fmt.Sprintf(mqttMaxPressureConfig, mqttSensor),
			generateMqttConfig(device, mqttMaxPressureTopic, "pressure", "Maximum Pressure", "hPa", "measurement"),
		},
	}

	for _, c := range configs {
		configPayload, err := json.Marshal(c.Config)
		if err != nil {
			log.Printf("Error marshalling config for %s: %v", c.Config.Name, err)
			continue
		}

		client.Publish(c.Topic, 0, true, configPayload).Wait()
		log.Printf("Home Assistant MQTT discovery config sent for %s", c.Config.Name)
	}
}

// Publish data to MQTT
func publishToMQTT(client mqtt.Client, topic string, value float64) {
	client.Publish(fmt.Sprintf(mqttAvail, mqttSensor), 0, true, "online").Wait()

	payload := fmt.Sprintf("%.2f", value)
	postTopic := fmt.Sprintf(topic, mqttSensor)
	client.Publish(postTopic, 0, false, payload).Wait()
	log.Printf("Published to %s: %.2f", postTopic, value)
}

// Connect to MQTT with retry mechanism
func connectToMQTT() mqtt.Client {
	opts := mqtt.NewClientOptions().
		AddBroker(mqttBroker).
		SetUsername(mqttUsername).
		SetPassword(mqttPassword).
		SetWill(fmt.Sprintf(mqttAvail, mqttSensor), "offline", 0, true). // Set the Will
		SetAutoReconnect(true)

	for i := 1; i <= maxRetries; i++ {
		client := mqtt.NewClient(opts)
		token := client.Connect()
		token.Wait()

		if token.Error() == nil {
			log.Println("Connected to MQTT broker")
			client.Publish(fmt.Sprintf(mqttAvail, mqttSensor), 0, true, "online").Wait() // Publish online status
			return client
		}

		log.Printf("Failed to connect to MQTT (attempt %d/%d): %v", i, maxRetries, token.Error())
		time.Sleep(retryDelay)
	}

	log.Fatal("Could not connect to MQTT broker after multiple attempts")
	return nil
}

func main() {
	setupLogging()
	log.Println("Starting Weather Sensor MQTT Publisher...")

	// Print environment variables for debugging
	log.Printf("Connecting to InfluxDB at: %s (Org: %s, Bucket: %s)", influxURL, influxOrg, influxBucket)
	log.Printf("Connecting to MQTT Broker: %s", mqttBroker)

	client := connectToMQTT()
	defer client.Disconnect(250)

	// Publish MQTT Discovery Config at startup
	publishMqttConfig(client)

	// Launch background goroutine for publishing config every 12 hours
	go func() {
		for {
			time.Sleep(configPublishInterval)
			log.Println("Republishing MQTT config...")
			publishMqttConfig(client)
		}
	}()

	// Example usage
	sensorType := extractSensorType(mqttMaxTempTopic)
	fmt.Println(sensorType) // Output: temperature-max

	// Main loop: Publish sensor data every 2 minutes
	log.Println("Entering MQTT publishing loop...")
	for {
		rainValue, err := queryInfluxDB("rain", "sum")
		if err != nil {
			log.Printf("Error querying rain data: %v", err)
		}

		windValue, err := queryInfluxDB("wind", "max")
		if err != nil {
			log.Printf("Error querying wind data: %v", err)
		}

		windGustValue, err := queryInfluxDB("wind-gust", "max")
		if err != nil {
			log.Printf("Error querying wind gust data: %v", err)
		}

		minTempValue, err := queryInfluxDB("temperature", "min")
		if err != nil {
			log.Printf("Error querying min temp data: %v", err)
		}

		maxTempValue, err := queryInfluxDB("temperature", "max")
		if err != nil {
			log.Printf("Error querying max temp data: %v", err)
		}

		minHumidityValue, err := queryInfluxDB("humidity", "min")
		if err != nil {
			log.Printf("Error querying min humidity data: %v", err)
		}

		maxHumidityValue, err := queryInfluxDB("humidity", "max")
		if err != nil {
			log.Printf("Error querying max humidity data: %v", err)
		}

		minPressureValue, err := queryInfluxDB("pressure", "min")
		if err != nil {
			log.Printf("Error querying min pressure data: %v", err)
		}

		maxPressureValue, err := queryInfluxDB("pressure", "max")
		if err != nil {
			log.Printf("Error querying max pressure data: %v", err)
		}

		publishToMQTT(client, mqttRainTopic, rainValue)
		publishToMQTT(client, mqttWindTopic, windValue)
		publishToMQTT(client, mqttWindGustTopic, windGustValue)

		publishToMQTT(client, mqttMinTempTopic, minTempValue)
		publishToMQTT(client, mqttMaxTempTopic, maxTempValue)

		publishToMQTT(client, mqttMinHumidTopic, minHumidityValue)
		publishToMQTT(client, mqttMaxHumidTopic, maxHumidityValue)

		publishToMQTT(client, mqttMinPressureTopic, minPressureValue)
		publishToMQTT(client, mqttMaxPressureTopic, maxPressureValue)

		time.Sleep(publishInterval)
	}
}
