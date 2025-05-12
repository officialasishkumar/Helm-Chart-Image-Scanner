# Helm Chart Image Scanner

## Overview

This is a Go-based web service that scans Helm charts to extract and analyze container images.

## Features

- Accepts a Helm chart URL via a POST request
- Extracts all container images from the chart's YAML files
- Retrieves detailed information about each image, including:
  - Full image reference
  - Total image size
  - Number of image layers

## Endpoints

### `/scan`

- **Method**: POST
- **Request Body**: 
  ```json
  {
    "chart_url": "https://example.com/mychart.tgz"
  }
  ```
- **Response**: JSON array of image details
  ```json
  [
    {
      "image": "nginx:latest",
      "size_bytes": 123456789,
      "layers": 5
    }
  ]
  ```

## How It Works

1. Downloads the Helm chart from the provided URL
2. Extracts and parses YAML files within the chart
3. Identifies unique container images
4. Pulls and inspects each image
5. Returns image metadata

## Requirements

- Go 1.16+
- Internet access to pull container images
- Dependencies:
  - `github.com/google/go-containerregistry/pkg/crane`
  - `gopkg.in/yaml.v3`

## Running the Service

```bash
go run main.go
```

The service will start on port 8080.

## Making API Calls

### cURL Example

```bash
curl -X POST http://localhost:8080/scan \
     -H "Content-Type: application/json" \
     -d '{"chart_url": "https://charts.bitnami.com/bitnami/wordpress-15.0.0.tgz"}'
```

## Error Handling

- Returns JSON error responses for invalid requests
- Skips images that cannot be pulled or inspected
- Provides detailed error messages

## Limitations

- Requires network access to container registries
- 2-minute timeout per image inspection
- Concurrent image scanning limited to 5 images at a time