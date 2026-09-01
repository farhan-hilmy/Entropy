# Toxiproxy Controller

Developer-focused UI for managing and injecting network failures with Toxiproxy.

## Quick Start

1. Ensure you have Docker and Docker Compose installed.
2. Run the application locally:
   ```bash
   docker compose up
   ```
3. Open http://localhost:3000 in your browser.

## Features

- **Dashboard**: View Toxiproxy connection status and overall proxy activity.
- **Proxy Management**: Create, view, edit, and delete proxies.
- **Toxic Management**: Add, edit, enable, disable, and delete toxics for each proxy.

## Architecture

- **Frontend**: React application for managing proxies and toxics.
- **Backend**: Node.js/Express server acting as an abstraction layer over the Toxiproxy API.
- **Toxiproxy**: The chaos engine.

## API

The backend exposes the following REST endpoints:

- `GET /api/proxies`
- `POST /api/proxies`
- ... (and more)
