package pkg

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type Server struct {
	router     *gin.Engine
	port       int
	handlers   []handler
	middleware []gin.HandlerFunc
}

type ServerBuilder struct {
	handlers   []handler
	middleware []gin.HandlerFunc
	port       int
	withPing   bool
}

func New() *ServerBuilder {
	return &ServerBuilder{
		handlers:   make([]handler, 0),
		middleware: make([]gin.HandlerFunc, 0),
		port:       8080,
		withPing:   false,
	}
}

type handler interface {
	RegisterRoutes(router *gin.Engine)
}

func (b *ServerBuilder) Handlers(handlers ...handler) *ServerBuilder {
	b.handlers = append(b.handlers, handlers...)
	return b
}

func (b *ServerBuilder) Middleware(middleware ...gin.HandlerFunc) *ServerBuilder {
	b.middleware = append(b.middleware, middleware...)
	return b
}

func (b *ServerBuilder) WithPing() *ServerBuilder {
	b.withPing = true
	return b
}

func (b *ServerBuilder) Port(port int) *ServerBuilder {
	b.port = port
	return b
}

func (b *ServerBuilder) WithCORS(origins ...string) *ServerBuilder {
	allowedOrigins := "*"
	if len(origins) > 0 {
		allowedOrigins = origins[0]
	}

	b.middleware = append(b.middleware, func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", allowedOrigins)
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})
	return b
}

func (b *ServerBuilder) WithRequestLogger() *ServerBuilder {
	b.middleware = append(b.middleware, func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()
		method := c.Request.Method
		clientIP := c.ClientIP()

		if raw != "" {
			path = path + "?" + raw
		}

		log.Printf("[%s] %s %s %d %v %s",
			method,
			path,
			clientIP,
			statusCode,
			latency,
			c.Errors.String(),
		)
	})
	return b
}

func (b *ServerBuilder) WithRequestTimeout(timeout time.Duration) *ServerBuilder {
	b.middleware = append(b.middleware, func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		finished := make(chan struct{})
		go func() {
			c.Next()
			close(finished)
		}()

		select {
		case <-finished:
			return
		case <-ctx.Done():
			c.AbortWithStatusJSON(http.StatusRequestTimeout, gin.H{
				"error": "request timeout",
			})
		}
	})
	return b
}

func (b *ServerBuilder) WithRateLimiter(requestsPerSecond int) *ServerBuilder {
	ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))

	b.middleware = append(b.middleware, func(c *gin.Context) {
		select {
		case <-ticker.C:
			c.Next()
		default:
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
		}
	})
	return b
}

func (b *ServerBuilder) WithRecovery() *ServerBuilder {
	b.middleware = append(b.middleware, func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Panic recovered: %v", err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "internal server error",
				})
			}
		}()
		c.Next()
	})
	return b
}

func (b *ServerBuilder) Build() *Server {
	router := gin.Default()

	for _, mw := range b.middleware {
		router.Use(mw)
	}

	if b.withPing {
		router.GET("/ping", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"message": "pong"})
		})
	}

	for _, h := range b.handlers {
		h.RegisterRoutes(router)
	}

	return &Server{
		router:     router,
		port:       b.port,
		handlers:   b.handlers,
		middleware: b.middleware,
	}
}

func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%d", s.port)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	go func() {
		log.Printf("Starting server on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}
