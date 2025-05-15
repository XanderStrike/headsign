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
	"strconv"
	"strings"
	"sync"
	"time"

	gtfsrealtime "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// VehicleData struct for our JSON endpoint (remains as is for now)
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
	VehicleTimestamp    string  `json:"vehicleTimestamp,omitempty"`
	StopID              string  `json:"stopId,omitempty"`
	RouteShortName      string  `json:"routeShortName,omitempty"`
	RouteLongName       string  `json:"routeLongName,omitempty"`
	RouteColor          string  `json:"routeColor,omitempty"`
	RouteTextColor      string  `json:"routeTextColor,omitempty"`
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

// Environment variables
var (
	swiftlyAPIKey string // To store the API key from env
	gtfsURL       string // To store the GTFS URL from env
)

const (
	cacheDuration  = 10 * time.Second // Adjusted to be just below 180 requests per 15 minutes (1 req / 5 sec)
	routesFilePath = "metro/routes.txt"
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

func loadRoutesData() {
	file, err := os.Open(routesFilePath)
	if err != nil {
		log.Printf("Error opening routes.txt: %v. Route information will be unavailable.", err)
		routesData = make(map[string]RouteInfo)
		return
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ','
	reader.LazyQuotes = true

	records, err := reader.ReadAll()
	if err != nil {
		log.Printf("Error reading CSV from routes.txt: %v. Route information will be unavailable.", err)
		routesData = make(map[string]RouteInfo)
		return
	}

	if len(records) < 2 {
		log.Println("routes.txt is empty or has no data rows. Route information will be unavailable.")
		routesData = make(map[string]RouteInfo)
		return
	}

	header := records[0]
	colIndex := make(map[string]int)
	const utf8BOM = "\xef\xbb\xbf"
	for i, colName := range header {
		cleanColName := colName
		if i == 0 {
			cleanColName = strings.TrimPrefix(cleanColName, utf8BOM)
		}
		colIndex[strings.TrimSpace(cleanColName)] = i
	}

	requiredCols := []string{"route_id", "route_short_name", "route_long_name", "route_color", "route_text_color"}
	for _, col := range requiredCols {
		if _, ok := colIndex[col]; !ok {
			log.Printf("routes.txt is missing required column: %s. Route information may be incomplete.", col)
		}
	}

	routesData = make(map[string]RouteInfo, len(records)-1)
	for i, record := range records {
		if i == 0 {
			continue
		}
		if len(record) != len(header) {
			log.Printf("Skipping malformed row %d in routes.txt: expected %d fields, got %d", i+1, len(header), len(record))
			continue
		}

		routeID, ok := colIndex["route_id"]
		if !ok {
			continue
		}

		shortName := colIndex["route_short_name"]
		longName := colIndex["route_long_name"]
		desc := colIndex["route_desc"]
		routeType := colIndex["route_type"]
		color := colIndex["route_color"]
		textColor := colIndex["route_text_color"]
		url := colIndex["route_url"]

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

func fetchAndProcessGTFSData(apiKey string) ([]VehicleData, error) {
	routesDataOnce.Do(loadRoutesData)
	log.Println("Fetching fresh data from API...")
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", gtfsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Accept", "application/x-protobuf")
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

	gtfsData := &gtfsrealtime.FeedMessage{}
	err = proto.Unmarshal(body, gtfsData)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling protobuf: %w", err)
	}

	var processedVehicles []VehicleData
	for _, entity := range gtfsData.GetEntity() {
		if entity.GetVehicle() != nil && entity.GetVehicle().GetTrip() != nil && entity.GetVehicle().GetPosition() != nil {
			vehicle := entity.GetVehicle()
			trip := vehicle.GetTrip()
			position := vehicle.GetPosition()
			vehicleDesc := vehicle.GetVehicle()

			label := ""
			if vehicleDesc != nil {
				label = vehicleDesc.GetLabel()
			}
			if label == "" {
				label = entity.GetId()
			}

			speed := 0.0
			if position.Speed != nil {
				speed = float64(position.GetSpeed())
			}

			vehicleTimestamp := ""
			if vehicle.Timestamp != nil {
				vehicleTimestamp = strconv.FormatUint(vehicle.GetTimestamp(), 10)
			}

			currentStopSequence := 0
			if vehicle.CurrentStopSequence != nil {
				currentStopSequence = int(vehicle.GetCurrentStopSequence())
			}

			var currentStatusStr string
			if vehicle.CurrentStatus != nil {
				currentStatusStr = vehicle.GetCurrentStatus().String()
			}

			vehicleData := VehicleData{
				ID:                  entity.GetId(),
				Label:               label,
				Latitude:            float64(position.GetLatitude()),
				Longitude:           float64(position.GetLongitude()),
				Speed:               speed,
				CurrentStopSequence: currentStopSequence,
				CurrentStatus:       currentStatusStr,
				VehicleTimestamp:    vehicleTimestamp,
				StopID:              vehicle.GetStopId(),
			}

			if trip != nil {
				vehicleData.TripID = trip.GetTripId()
				vehicleData.RouteID = trip.GetRouteId()
				vehicleData.DirectionID = int(trip.GetDirectionId())
				vehicleData.StartDate = trip.GetStartDate()
				vehicleData.StartTime = trip.GetStartTime()

				if routeInfo, ok := routesData[trip.GetRouteId()]; ok {
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

func mapHandler(w http.ResponseWriter, r *http.Request) {
	err := tmpl.Execute(w, nil)
	if err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Error generating map. Please try again later.", http.StatusInternalServerError)
		return
	}
}

func routesHandler(w http.ResponseWriter, r *http.Request) {
	routesDataOnce.Do(loadRoutesData)

	var routesList []RouteInfo
	for _, route := range routesData {
		routesList = append(routesList, route)
	}

	w.Header().Set("Content-Type", "application/json")
	err := json.NewEncoder(w).Encode(routesList)
	if err != nil {
		log.Printf("Error encoding routes data to JSON: %v", err)
		http.Error(w, "Error encoding data", http.StatusInternalServerError)
		return
	}
}

func main() {
	var err error

	// Get environment variables
	swiftlyAPIKey = os.Getenv("SWIFTLY_API_KEY")
	if swiftlyAPIKey == "" {
		log.Println("Error: SWIFTLY_API_KEY environment variable not set.")
		os.Exit(1)
	}

	gtfsURL = os.Getenv("GTFS_URL")
	if gtfsURL == "" {
		log.Println("Error: GTFS_URL environment variable not set.")
		os.Exit(1)
	}

	tmpl, err = template.ParseFiles("map_template.html")
	if err != nil {
		log.Printf("Error parsing template: %v", err)
		os.Exit(1)
	}

	routesDataOnce.Do(loadRoutesData)

	http.HandleFunc("/", mapHandler)
	http.HandleFunc("/vehicledata", vehicleDataHandler)
	http.HandleFunc("/routes", routesHandler)

	port := "8080"
	log.Printf("Server starting on port %s. Access map at /, vehicle data at /vehicledata, and routes data at /routes. Make sure SWIFTLY_API_KEY is set.", port)
	err = http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Printf("Error starting server: %v", err)
		os.Exit(1)
	}
}
