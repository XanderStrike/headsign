# Headsign

A very important website.

## Run your own

Follow the example of docker-compose.yml, make sure to set up a `.env` file based on the example.

## API

This is lowkey an API gateway.

*   `/`: Serves the `speed.html` file, which is likely a map display.
*   `/vehicledata`: Returns JSON data about vehicle positions, speeds, and route information. This data is fetched from a GTFS realtime feed and cached.
*   `/routes`: Returns JSON data about all available routes, loaded from `metro/routes.txt`.
*   `/stops`: Returns JSON data about all available stops, loaded from `metro/stops.txt`.

The server requires `SWIFTLY_API_KEY` and `GTFS_URL` environment variables to be set.
