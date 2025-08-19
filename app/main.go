package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newExporter(ctx context.Context) (*otlptrace.Exporter, error) {
	// Get New Relic OTLP endpoint from environment variable or use default
	endpoint := os.Getenv("NEW_RELIC_OTLP_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("NEW_RELIC_OTLP_ENDPOINT environment variable is required")
	}

	// Get New Relic API key from environment variable
	apiKey := os.Getenv("NEW_RELIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("NEW_RELIC_API_KEY environment variable is required")
	}

	log.Printf("Initializing OpenTelemetry with New Relic endpoint: %s", endpoint)

	// Create OTLP trace exporter with New Relic configuration
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithHeaders(map[string]string{
			"Accept":  "*/*",
			"api-key": apiKey,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	return exporter, nil
}

func newTracerProvider(exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	// Create resource with service information
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("go-app"),
		),
	)
	if err != nil {
		panic(err)
	}

	// Create TracerProvider
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
}

func getHealtz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func getRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the chi HTTP server behind Nginx!")
}

func getHello(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "World"
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
}

func getUserByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "profile": map[string]any{"nickname": "guest", "created_at": time.Now().UTC()}})
}

func main() {
	// Initialize OpenTelemetry
	ctx := context.Background()

	exp, err := newExporter(ctx)
	if err != nil {
		log.Fatalf("failed to create exporter: %v", err)
	}

	tp := newTracerProvider(exp)

	defer func() { _ = tp.Shutdown(ctx) }()

	otel.SetTracerProvider(tp)

	tracer = tp.Tracer("go-app")

	// Create chi router
	r := chi.NewRouter()

	r.Use(otelchi.Middleware("go-app"))

	// Define routes
	r.Get("/healthz", getHealtz)
	r.Get("/", getRoot)
	r.Get("/hello", getHello)
	r.Get("/users/{id}", getUserByID)

	log.Fatal(http.ListenAndServe(":8080", r))
}
