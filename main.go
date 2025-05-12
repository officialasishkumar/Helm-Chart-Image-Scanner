package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
	"gopkg.in/yaml.v3"
)

type scanRequest struct {
	ChartURL string `json:"chart_url"`
}

type ImageInfo struct {
	Image     string `json:"image"`
	SizeBytes int64  `json:"size_bytes"`
	NumLayers int    `json:"layers"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	http.HandleFunc("/scan", scanHandler)
	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func scanHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST allowed", http.StatusMethodNotAllowed)
		return
	}
	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ChartURL == "" {
		jsonError(w, http.StatusBadRequest, "chart_url is required")
		return
	}

	images, err := scanChartForImages(req.ChartURL)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("scan failed: %v", err))
		return
	}

	if images == nil {
		images = make([]ImageInfo, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(images)
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

func scanChartForImages(chartURL string) ([]ImageInfo, error) {
	resp, err := http.Get(chartURL)
	if err != nil {
		return nil, fmt.Errorf("downloading chart: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status downloading chart: %s", resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	foundImages := make(map[string]struct{})

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if !strings.HasSuffix(hdr.Name, ".yaml") && !strings.HasSuffix(hdr.Name, ".yml") {
			continue
		}
		buf := make([]byte, hdr.Size)
		if _, err := io.ReadFull(tr, buf); err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		imgs, _ := extractImagesFromYAML(buf)
		for _, img := range imgs {
			foundImages[img] = struct{}{}
		}
	}

	imageList := make([]string, 0, len(foundImages))
	for img := range foundImages {
		imageList = append(imageList, img)
	}

	type res struct {
		info ImageInfo
		err  error
	}
	results := make(chan res, len(imageList))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)

	for _, img := range imageList {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			sem <- struct{}{}
			info, err := inspectImage(ref)
			<-sem
			results <- res{info, err}
		}(img)
	}
	wg.Wait()
	close(results)

	var out []ImageInfo
	for r := range results {
		if r.err != nil {
			log.Printf("warning: failed %q: %v", r.info.Image, r.err)
			continue
		}
		out = append(out, r.info)
	}
	return out, nil
}

func extractImagesFromYAML(data []byte) ([]string, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	imgs := make(map[string]struct{})
	for {
		var doc interface{}
		if err := dec.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		scanNode(doc, imgs)
	}
	list := make([]string, 0, len(imgs))
	for img := range imgs {
		list = append(list, img)
	}
	return list, nil
}

func scanNode(node interface{}, imgs map[string]struct{}) {
	switch v := node.(type) {
	case map[string]interface{}:
		// 1) image: "<string>"
		if iv, ok := v["image"]; ok {
			switch x := iv.(type) {
			case string:
				imgs[x] = struct{}{}
			case map[string]interface{}:
				if built := buildFromMap(x); built != "" {
					imgs[built] = struct{}{}
				}
			}
		}
		// 2) repository + tag at same level
		if rv, ok1 := v["repository"]; ok1 {
			if tv, ok2 := v["tag"]; ok2 {
				if repo, ok := rv.(string); ok {
					if tag, ok := tv.(string); ok {
						imgs[repo+":"+tag] = struct{}{}
					}
				}
			}
		}
		for _, child := range v {
			scanNode(child, imgs)
		}
	case []interface{}:
		for _, e := range v {
			scanNode(e, imgs)
		}
	}
}

func buildFromMap(m map[string]interface{}) string {
	reg, _ := m["registry"].(string)
	repo, _ := m["repository"].(string)
	if repo == "" {
		repo, _ = m["name"].(string)
	}
	if repo == "" {
		return ""
	}
	tag, _ := m["tag"].(string)
	digest, _ := m["digest"].(string)

	img := strings.TrimRight(reg, "/")
	if img != "" {
		img += "/" + repo
	} else {
		img = repo
	}
	if digest != "" {
		img += "@" + digest
	} else if tag != "" {
		img += ":" + tag
	}
	return img
}

func inspectImage(ref string) (ImageInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	img, err := crane.Pull(ref, crane.WithContext(ctx))
	if err != nil {
		return ImageInfo{Image: ref}, err
	}
	layers, err := img.Layers()
	if err != nil {
		return ImageInfo{Image: ref}, err
	}
	var total int64
	for _, l := range layers {
		sz, err := l.Size()
		if err != nil {
			return ImageInfo{Image: ref}, err
		}
		total += sz
	}
	return ImageInfo{Image: ref, SizeBytes: total, NumLayers: len(layers)}, nil
}
