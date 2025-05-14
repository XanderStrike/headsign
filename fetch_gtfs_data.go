package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"html/template"
	"sync"
	"time"
)

// Structs to match the JSON structure
type GtfsResponse struct {
	Header struct {
		GtfsRealtimeVersion string `json:"gtfsRealtimeVersion"`
		Incrementality      string `json:"incrementality"`
		Timestamp           string `json:"timestamp"`
	} `json:"header"`
	Entity []Entity `json:"entity"`
}

type Entity struct {
	ID      string  `json:"id"`
	Vehicle Vehicle `json:"vehicle"`
}

type Vehicle struct {
	Trip     *Trip     `json:"trip,omitempty"` // Pointer to allow null
	Position *Position `json:"position,omitempty"` // Pointer to allow null
	Vehicle  struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"vehicle"`
	CurrentStopSequence int    `json:"currentStopSequence,omitempty"`
	CurrentStatus       string `json:"currentStatus,omitempty"`
	Timestamp           string `json:"timestamp,omitempty"`
	StopID              string `json:"stopId,omitempty"`
}

type Trip struct {
	TripID               string `json:"tripId"`
	StartTime            string `json:"startTime"`
	StartDate            string `json:"startDate"`
	ScheduleRelationship string `json:"scheduleRelationship"`
	RouteID              string `json:"routeId"`
	DirectionID          int    `json:"directionId"`
}

type Position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Speed     float64 `json:"speed,omitempty"`
}

// Global template variable
var tmpl *template.Template
var swiftlyAPIKey string // To store the API key from env

const (
	gtfsURL       = "https://api.goswift.ly/real-time/lametro/gtfs-rt-vehicle-positions?format=json"
	cacheDuration = 30 * time.Second
)

type TemplateData struct {
	Markers template.JS
}

// CachedResponse holds the data and its timestamp
type CachedResponse struct {
	Data      TemplateData
	Timestamp time.Time
}

var (
	apiCache   CachedResponse
	cacheMutex sync.Mutex
)

func fetchAndProcessGTFSData(apiKey string) (TemplateData, error) {
	log.Println("Fetching fresh data from API...") // Log when we hit the actual API
	client := &http.Client{Timeout: 10 * time.Second} // Added a timeout for the client
	req, err := http.NewRequest("GET", gtfsURL, nil)
	if err != nil {
		return TemplateData{}, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", apiKey) // Use the passed-in apiKey

	resp, err := client.Do(req)
	if err != nil {
		return TemplateData{}, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return TemplateData{}, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return TemplateData{}, fmt.Errorf("error reading response body: %w", err)
	}

	var gtfsData GtfsResponse
	err = json.Unmarshal(body, &gtfsData)
	if err != nil {
		return TemplateData{}, fmt.Errorf("error unmarshalling JSON: %w", err)
	}

	var markers []string
	for _, entity := range gtfsData.Entity {
		if entity.Vehicle.Trip != nil && entity.Vehicle.Position != nil {
			lat := entity.Vehicle.Position.Latitude
			lon := entity.Vehicle.Position.Longitude
			label := entity.Vehicle.Vehicle.Label
			if label == "" {
				label = entity.ID
			}
			jsLabel := html.EscapeString(label)
			// Get speed, default to 0 if not available
			speed := 0.0
			if entity.Vehicle.Position.Speed != 0 {
				speed = entity.Vehicle.Position.Speed
			}
			popupContent := fmt.Sprintf("<b>Vehicle ID:</b> %s<br><b>Speed:</b> %.2f m/s", jsLabel, speed)
			markers = append(markers, fmt.Sprintf("L.marker([%s, %s]).addTo(map).bindPopup('%s');", strconv.FormatFloat(lat, 'f', -1, 64), strconv.FormatFloat(lon, 'f', -1, 64), popupContent))
		}
	}
	markerScript := strings.Join(markers, "\n")
	return TemplateData{Markers: template.JS(markerScript)}, nil
}

func getCachedGTFSData(apiKey string) (TemplateData, error) {
	cacheMutex.Lock()
	// Check if cache is valid
	if !apiCache.Timestamp.IsZero() && time.Since(apiCache.Timestamp) < cacheDuration {
		log.Println("Serving from cache.")
		data := apiCache.Data
		cacheMutex.Unlock()
		return data, nil
	}
	cacheMutex.Unlock() // Unlock before potentially long operation

	// Cache is invalid or expired, fetch new data
	newData, err := fetchAndProcessGTFSData(apiKey)
	if err != nil {
		return TemplateData{}, err
	}

	// Update cache
	cacheMutex.Lock()
	apiCache = CachedResponse{
		Data:      newData,
		Timestamp: time.Now(),
	}
	cacheMutex.Unlock()
	log.Println("Cache updated.")
	return newData, nil
}

func mapHandler(w http.ResponseWriter, r *http.Request) {
	data, err := getCachedGTFSData(swiftlyAPIKey) // Use the caching layer
	if err != nil {
		log.Printf("Error fetching GTFS data: %v", err)
		http.Error(w, "Error fetching vehicle data. Please try again later.", http.StatusInternalServerError)
		return
	}

	err = tmpl.Execute(w, data)
	if err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Error generating map. Please try again later.", http.StatusInternalServerError)
		return
	}
}

func main() {
	var err error

	// Get API Key from environment variable
	swiftlyAPIKey = os.Getenv("SWIFTLY_API_KEY")
	if swiftlyAPIKey == "" {
		log.Println("Error: SWIFTLY_API_KEY environment variable not set.")
		os.Exit(1)
	}

	// Parse the HTML template once at startup
	tmpl, err = template.ParseFiles("/Users/xander/workspace/headsign/map_template.html")
	if err != nil {
		log.Printf("Error parsing template: %v", err)
		os.Exit(1) // If template doesn't parse, we can't serve requests
	}

	http.HandleFunc("/", mapHandler)

	port := "8080"
	log.Printf("Server starting on port %s. Make sure SWIFTLY_API_KEY is set.", port)
	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Printf("Error starting server: %v", err)
		os.Exit(1)
	}
}
