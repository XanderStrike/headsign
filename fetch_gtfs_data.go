package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Structs to match the JSON structure from GTFS-RT
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
	Trip     *Trip     `json:"trip,omitempty"`
	Position *Position `json:"position,omitempty"`
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

// VehicleData struct for our JSON endpoint
type VehicleData struct {
	ID                  string  `json:"id"`
	Label               string  `json:"label"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
	Speed               float64 `json:"speed"`
	TripID              string  `json:"tripId,omitempty"`
	RouteID             string  `json:"routeId,omitempty"`
	DirectionID         int     `json:"directionId,omitempty"`
	StartDate           string  `json:"startDate,omitempty"`
	StartTime           string  `json:"startTime,omitempty"`
	CurrentStopSequence int     `json:"currentStopSequence,omitempty"`
	CurrentStatus       string  `json:"currentStatus,omitempty"`
	VehicleTimestamp    string  `json:"vehicleTimestamp,omitempty"` // Renamed to avoid conflict with cache timestamp
	StopID              string  `json:"stopId,omitempty"`
	// Fields from routes.txt
	RouteShortName string `json:"routeShortName,omitempty"`
	RouteLongName  string `json:"routeLongName,omitempty"`
	RouteColor     string `json:"routeColor,omitempty"`
	RouteTextColor string `json:"routeTextColor,omitempty"`
}

// RouteInfo struct to hold data from routes.txt
type RouteInfo struct {
	RouteID     string
	ShortName   string
	LongName    string
	Description string
	Type        string
	Color       string
	TextColor   string
	URL         string
}

// Global route data
var routesData map[string]RouteInfo
var routesDataOnce sync.Once

// Global template variable
var tmpl *template.Template
var swiftlyAPIKey string // To store the API key from env

const (
	gtfsURL        = "https://api.goswift.ly/real-time/lametro/gtfs-rt-vehicle-positions?format=json"
	cacheDuration  = 10 * time.Second // Adjusted to be just below 180 requests per 15 minutes (1 req / 5 sec)
	routesFilePath = "/Users/xander/workspace/headsign/gtfs_bus/routes.txt"
)

// CachedVehicleData holds the processed vehicle data and its timestamp
type CachedVehicleData struct {
	Data      []VehicleData
	Timestamp time.Time
}

var (
	vehicleCache CachedVehicleData
	cacheMutex   sync.Mutex
)

// loadRoutesData loads and parses the routes.txt file.
// It's designed to be called once using sync.Once.
func loadRoutesData() {
	file, err := os.Open(routesFilePath)
	if err != nil {
		log.Printf("Error opening routes.txt: %v. Route information will be unavailable.", err)
		routesData = make(map[string]RouteInfo) // Initialize to empty map on error
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ','       // Ensure comma is the delimiter
	reader.LazyQuotes = true // Handle quotes liberally

	records, err := reader.ReadAll()
	if err != nil {
		log.Printf("Error reading CSV from routes.txt: %v. Route information will be unavailable.", err)
		routesData = make(map[string]RouteInfo) // Initialize to empty map on error
		return
	}

	if len(records) < 2 { // Expect at least a header and one data row
		log.Println("routes.txt is empty or has no data rows. Route information will be unavailable.")
		routesData = make(map[string]RouteInfo)
		return
	}

	header := records[0]
	colIndex := make(map[string]int)
	// Define UTF-8 BOM
	const utf8BOM = "\xef\xbb\xbf"
	for i, colName := range header {
		// Remove BOM if present from the first column name, then trim space
		cleanColName := colName
		if i == 0 {
			cleanColName = strings.TrimPrefix(cleanColName, utf8BOM)
		}
		colIndex[strings.TrimSpace(cleanColName)] = i
	}

	// Verify necessary columns exist
	requiredCols := []string{"route_id", "route_short_name", "route_long_name", "route_color", "route_text_color"}
	for _, col := range requiredCols {
		if _, ok := colIndex[col]; !ok {
			log.Printf("routes.txt is missing required column: %s. Route information may be incomplete.", col)
		}
	}

	routesData = make(map[string]RouteInfo, len(records)-1)
	for i, record := range records {
		if i == 0 { // Skip header row
			continue
		}
		if len(record) != len(header) {
			log.Printf("Skipping malformed row %d in routes.txt: expected %d fields, got %d", i+1, len(header), len(record))
			continue
		}

		routeID, ok := colIndex["route_id"]
		if !ok {
			continue
		} // Should have been caught by verify

		shortName, _ := colIndex["route_short_name"]
		longName, _ := colIndex["route_long_name"]
		desc, _ := colIndex["route_desc"]
		routeType, _ := colIndex["route_type"]
		color, _ := colIndex["route_color"]
		textColor, _ := colIndex["route_text_color"]
		url, _ := colIndex["route_url"]

		routesData[record[routeID]] = RouteInfo{
			RouteID:     record[routeID],
			ShortName:   record[shortName],
			LongName:    record[longName],
			Description: record[desc],
			Type:        record[routeType],
			Color:       record[color],
			TextColor:   record[textColor],
			URL:         record[url],
		}
	}
	log.Printf("Successfully loaded %d routes from %s", len(routesData), routesFilePath)
}

// fetchAndProcessGTFSData fetches data from GTFS API and processes it into []VehicleData
func fetchAndProcessGTFSData(apiKey string) ([]VehicleData, error) {
	routesDataOnce.Do(loadRoutesData) // Ensure routesData is loaded only once
	log.Println("Fetching fresh data from API...")
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", gtfsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	var gtfsData GtfsResponse
	err = json.Unmarshal(body, &gtfsData)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling JSON: %w", err)
	}

	var processedVehicles []VehicleData
	for _, entity := range gtfsData.Entity {
		if entity.Vehicle.Trip != nil && entity.Vehicle.Position != nil {
			label := entity.Vehicle.Vehicle.Label
			if label == "" {
				label = entity.ID // Use entity ID if vehicle label is empty
			}
			speed := 0.0
			if entity.Vehicle.Position.Speed != 0 { // Check if Speed is non-zero
				speed = entity.Vehicle.Position.Speed
			}
			vehicleData := VehicleData{
				ID:                  entity.ID,
				Label:               label,
				Latitude:            entity.Vehicle.Position.Latitude,
				Longitude:           entity.Vehicle.Position.Longitude,
				Speed:               speed,
				CurrentStopSequence: entity.Vehicle.CurrentStopSequence,
				CurrentStatus:       entity.Vehicle.CurrentStatus,
				VehicleTimestamp:    entity.Vehicle.Timestamp,
				StopID:              entity.Vehicle.StopID,
			}
			if entity.Vehicle.Trip != nil {
				vehicleData.TripID = entity.Vehicle.Trip.TripID
				vehicleData.RouteID = entity.Vehicle.Trip.RouteID
				vehicleData.DirectionID = entity.Vehicle.Trip.DirectionID
				vehicleData.StartDate = entity.Vehicle.Trip.StartDate
				vehicleData.StartTime = entity.Vehicle.Trip.StartTime

				// Populate route information if available
				if routeInfo, ok := routesData[entity.Vehicle.Trip.RouteID]; ok {
					vehicleData.RouteShortName = routeInfo.ShortName
					vehicleData.RouteLongName = routeInfo.LongName
					vehicleData.RouteColor = routeInfo.Color
					vehicleData.RouteTextColor = routeInfo.TextColor
				}
			}
			processedVehicles = append(processedVehicles, vehicleData)
		}
	}
	return processedVehicles, nil
}

// getCachedVehicleData retrieves vehicle data, using cache if valid
func getCachedVehicleData(apiKey string) ([]VehicleData, error) {
	cacheMutex.Lock()
	if !vehicleCache.Timestamp.IsZero() && time.Since(vehicleCache.Timestamp) < cacheDuration {
		log.Println("Serving vehicle data from cache.")
		data := vehicleCache.Data
		cacheMutex.Unlock()
		return data, nil
	}
	cacheMutex.Unlock()

	newData, err := fetchAndProcessGTFSData(apiKey)
	if err != nil {
		return nil, err
	}

	cacheMutex.Lock()
	vehicleCache = CachedVehicleData{
		Data:      newData,
		Timestamp: time.Now(),
	}
	cacheMutex.Unlock()
	log.Println("Vehicle data cache updated.")
	return newData, nil
}

// vehicleDataHandler serves the vehicle data as JSON
func vehicleDataHandler(w http.ResponseWriter, r *http.Request) {
	data, err := getCachedVehicleData(swiftlyAPIKey)
	if err != nil {
		log.Printf("Error fetching vehicle data for JSON endpoint: %v", err)
		http.Error(w, "Error fetching vehicle data", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(data)
	if err != nil {
		log.Printf("Error encoding vehicle data to JSON: %v", err)
		http.Error(w, "Error encoding data", http.StatusInternalServerError)
		return
	}
}

// mapHandler serves the HTML page that will use JavaScript to fetch and display data
func mapHandler(w http.ResponseWriter, r *http.Request) {
	// No data needs to be passed to the template directly anymore for markers
	err := tmpl.Execute(w, nil)
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

	// Load routes data at startup
	routesDataOnce.Do(loadRoutesData)

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicledata", vehicleDataHandler) // New endpoint for JSON data

	port := "8080"
	log.Printf("Server starting on port %s. Access map at / and vehicle data at /vehicledata. Make sure SWIFTLY_API_KEY is set.", port)
	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Printf("Error starting server: %v", err)
		os.Exit(1)
	}
}
