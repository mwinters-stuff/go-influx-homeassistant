package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
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
	mqttSensor            = getEnv("MQTT_SENSOR", "weather-import")
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
	mqttRainTopic      = "homeassistant/sensor/%s/rain/state"
	mqttRainConfig     = "homeassistant/sensor/%s/rain/config"
	mqttWindTopic      = "homeassistant/sensor/%s/wind_max/state"
	mqttWindConfig     = "homeassistant/sensor/%s/wind_max/config"
	mqttWindGustTopic  = "homeassistant/sensor/%s/wind_gust_max/state"
	mqttWindGustConfig = "homeassistant/sensor/%s/wind_gust_max/config"
	mqttAvail          = "homeassistant/sensor/%s/weather/availability"
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
	UnitOfMeasurement   string `json:"unit_of_measurement"`
	ValueTemplate       string `json:"value_template"`
	UniqueID            string `json:"unique_id"`
	AvailabilityTopic   string `json:"availability_topic"`
	PayloadAvailable    string `json:"payload_available"`
	PayloadNotAvailable string `json:"payload_not_available"`
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
func queryInfluxDBRain() (float64, error) {
	log.Println("Querying InfluxDB for rain data...")
	return queryInfluxDBValue("sensor-data", "rain", "sum")
}

// Query InfluxDB for max wind speed since midnight
func queryInfluxDBMaxWind() (float64, error) {
	log.Println("Querying InfluxDB for max wind speed data...")
	return queryInfluxDBValue("sensor-data", "wind", "max")
}

// Query InfluxDB for max wind gust speed since midnight
func queryInfluxDBMaxWindGust() (float64, error) {
	log.Println("Querying InfluxDB for max wind gust speed data...")
	return queryInfluxDBValue("sensor-data", "wind-gust", "max")
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

// Publish MQTT Discovery Config for Home Assistant
func publishMqttConfig(client mqtt.Client) {
	log.Println("Publishing MQTT discovery config...")
	configs := []struct {
		Topic  string
		Config MqttConfig
	}{
		{
			Topic: mqttRainConfig,
			Config: MqttConfig{
				DeviceClass:         "precipitation",
				Name:                "Rainfall Sensor",
				StateTopic:          fmt.Sprintf(mqttRainTopic, mqttSensor),
				UnitOfMeasurement:   "mm",
				ValueTemplate:       "{{ value | float }}",
				UniqueID:            "sensor_rainfall",
				AvailabilityTopic:   fmt.Sprintf(mqttAvail, mqttSensor),
				PayloadAvailable:    "online",
				PayloadNotAvailable: "offline",
			},
		},
		{
			Topic: mqttWindConfig,
			Config: MqttConfig{
				DeviceClass:         "wind_speed",
				Name:                "Max Wind Speed",
				StateTopic:          fmt.Sprintf(mqttWindTopic, mqttSensor),
				UnitOfMeasurement:   "km/h",
				ValueTemplate:       "{{ value | float }}",
				UniqueID:            "sensor_wind_max",
				AvailabilityTopic:   fmt.Sprintf(mqttAvail, mqttSensor),
				PayloadAvailable:    "online",
				PayloadNotAvailable: "offline",
			},
		},
		{
			Topic: mqttWindConfig,
			Config: MqttConfig{
				DeviceClass:         "wind_speed",
				Name:                "Max Wind Gust Speed",
				StateTopic:          fmt.Sprintf(mqttWindGustTopic, mqttSensor),
				UnitOfMeasurement:   "km/h",
				ValueTemplate:       "{{ value | float }}",
				UniqueID:            "sensor_wind_gust_max",
				AvailabilityTopic:   fmt.Sprintf(mqttAvail, mqttSensor),
				PayloadAvailable:    "online",
				PayloadNotAvailable: "offline",
			},
		}}

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
	client.Publish(mqttAvail, 0, true, "online").Wait()
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
		SetPassword(mqttPassword)

	for i := 1; i <= maxRetries; i++ {
		client := mqtt.NewClient(opts)
		token := client.Connect()
		token.Wait()

		if token.Error() == nil {
			log.Println("Connected to MQTT broker")
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

	// Main loop: Publish sensor data every 2 minutes
	log.Println("Entering MQTT publishing loop...")
	for {
		rainValue, err := queryInfluxDBRain()
		if err != nil {
			log.Printf("Error querying rain data: %v", err)
		}

		windValue, err := queryInfluxDBMaxWind()
		if err != nil {
			log.Printf("Error querying wind data: %v", err)
		}

		windGustValue, err := queryInfluxDBMaxWindGust()
		if err != nil {
			log.Printf("Error querying wind gust data: %v", err)
		}

		publishToMQTT(client, mqttRainTopic, rainValue)
		publishToMQTT(client, mqttWindTopic, windValue)
		publishToMQTT(client, mqttWindGustTopic, windGustValue)

		time.Sleep(publishInterval)
	}
}
