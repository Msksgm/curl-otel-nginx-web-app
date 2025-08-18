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

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func initTracer(ctx context.Context) (*sdktrace.TracerProvider, error) {
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
			"api-key": apiKey,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Create resource with service information
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("go-app"),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("environment", "development"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Set global TracerProvider
	otel.SetTracerProvider(tp)

	// Set global propagator for context propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, nil
}

func main() {
	// Initialize OpenTelemetry
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tp, err := initTracer(ctx)
	if err != nil {
		log.Printf("WARNING: Failed to initialize tracer: %v", err)
		log.Printf("Running without tracing enabled")
	} else {
		log.Printf("OpenTelemetry tracer initialized successfully")
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := tp.Shutdown(shutdownCtx); err != nil {
				log.Printf("Error shutting down tracer provider: %v", err)
			}
		}()
	}

	tracer := otel.Tracer("go-app")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		// The context already contains trace information from otelhttp middleware
		ctx := r.Context()
		_, span := tracer.Start(ctx, "health_check")
		defer span.End()

		span.SetAttributes(attribute.String("http.target", r.URL.Path))
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// The context already contains trace information from otelhttp middleware
		ctx := r.Context()
		_, span := tracer.Start(ctx, "root_handler")
		defer span.End()

		span.SetAttributes(attribute.String("http.target", r.URL.Path))
		fmt.Fprintln(w, "Welcome to the standard library HTTP server behind Nginx!")
	})

	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		// The context already contains trace information from otelhttp middleware
		ctx := r.Context()
		_, span := tracer.Start(ctx, "hello_handler")
		defer span.End()

		name := r.URL.Query().Get("name")
		if name == "" {
			name = "World"
		}
		span.SetAttributes(
			attribute.String("http.target", r.URL.Path),
			attribute.String("hello.name", name),
		)
		writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
	})

	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		// The context already contains trace information from otelhttp middleware
		ctx := r.Context()
		_, span := tracer.Start(ctx, "get_user_handler")
		defer span.End()

		id := r.PathValue("id")
		span.SetAttributes(
			attribute.String("http.target", r.URL.Path),
			attribute.String("user.id", id),
		)
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "profile": map[string]any{"nickname": "guest", "created_at": time.Now().UTC()}})
	})

	// Wrap handler with OpenTelemetry HTTP instrumentation
	handler := otelhttp.NewHandler(logging(mux), "http-server")

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
