package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	// The context already contains trace information from otelhttp middleware
	ctx := r.Context()
	_, span := tracer.Start(ctx, "health_check")
	defer span.End()

	span.SetAttributes(attribute.String("http.target", r.URL.Path))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func getRoot(w http.ResponseWriter, r *http.Request) {
	// The context already contains trace information from otelhttp middleware
	ctx := r.Context()
	_, span := tracer.Start(ctx, "root_handler")
	defer span.End()

	span.SetAttributes(attribute.String("http.target", r.URL.Path))
	fmt.Fprintln(w, "Welcome to the chi HTTP server behind Nginx!")
}

func getHello(w http.ResponseWriter, r *http.Request) {
	// The context already contains trace information from otelhttp middleware
	ctx := r.Context()
	_, span := tracer.Start(ctx, "hello_handler")
	defer span.End()

	// Log trace information for debugging
	if spanCtx := span.SpanContext(); spanCtx.IsValid() {
		log.Printf("Handling /hello request - TraceID: %s, SpanID: %s",
			spanCtx.TraceID().String(), spanCtx.SpanID().String())
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		name = "World"
	}
	span.SetAttributes(
		attribute.String("http.target", r.URL.Path),
		attribute.String("hello.name", name),
	)
	writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
}

func getUserByID(w http.ResponseWriter, r *http.Request) {
	// The context already contains trace information from otelhttp middleware
	ctx := r.Context()
	_, span := tracer.Start(ctx, "get_user_handler")
	defer span.End()

	id := chi.URLParam(r, "id")
	span.SetAttributes(
		attribute.String("http.target", r.URL.Path),
		attribute.String("user.id", id),
	)
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

	// Add chi middleware for logging
	r.Use(middleware.Logger)

	// Define routes
	r.Get("/healthz", getHealtz)
	r.Get("/", getRoot)
	r.Get("/hello", getHello)
	r.Get("/users/{id}", getUserByID)

	// Wrap handler with OpenTelemetry HTTP instrumentation with proper options
	handler := otelhttp.NewHandler(
		r,
		"http-server",
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			// Create more descriptive span names based on the route
			return fmt.Sprintf("%s %s", r.Method, r.URL.Path)
		}),
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
		otelhttp.WithTracerProvider(otel.GetTracerProvider()),
		otelhttp.WithPropagators(otel.GetTextMapPropagator()),
	)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	log.Println("shutting down...")
	_ = srv.Shutdown(shutdownCtx)
}
