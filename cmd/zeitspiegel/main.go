// Command zeitspiegel wires the delay pipeline: capture → ring buffer →
// (engine → display | exporter | preview) + HTTP control plane. All wall
// clock usage of the system lives here and in the hardware adapters
// (hard rule 6).
package main

import (
	"context"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

type sysClock struct{}

func (sysClock) Now() time.Time { return time.Now() }

func run() error {
	configPath := flag.String("config", "", "path to config.toml (defaults apply when empty)")
	sourceFlag := flag.String("source", "", "override frame source: camera | synth")
	bindFlag := flag.String("bind", "", "override listen address")
	flag.Parse()

	cfg := config.Default()
	if *configPath != "" {
		var err error
		if cfg, err = config.Load(*configPath); err != nil {
			return err
		}
	}
	if *sourceFlag != "" {
		cfg.Source = *sourceFlag
	}
	if *bindFlag != "" {
		cfg.Bind = *bindFlag
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	start := time.Now()

	buf := ringbuf.New(time.Duration(cfg.BufferMaxS*float64(time.Second)), cfg.BufferMaxBytes)
	eng := engine.New(buf)

	clipDir := cfg.ClipDir
	if clipDir == "" {
		clipDir = os.TempDir()
	}
	// 3 export slots (ARCHITECTURE §3, IT-8)
	exporter := export.New(clipDir, 3)
	exportSeconds := expvar.NewFloat("zeitspiegel_export_seconds")
	clipper := &meteredClipper{
		inner: &httpapi.Clipper{Buffer: buf, Exporter: exporter, Clock: sysClock{}, FPS: cfg.FPS()},
		gauge: exportSeconds,
	}

	display, closeDisplay, err := openDisplay(cfg)
	if err != nil {
		return err
	}
	if closeDisplay != nil {
		defer closeDisplay()
	}

	restart := &atomic.Bool{}
	store := &runtimeStore{rt: cfg.Runtime(), buf: buf, restart: restart, setMirror: displayMirrorFunc(display)}

	sup := capture.New(capture.Options{
		Open: func(ctx context.Context) (capture.Source, error) {
			src, err := openSource(ctx, cfg, store.Current())
			if err != nil {
				return nil, err
			}
			return &restartable{Source: src, restart: restart}, nil
		},
		Push:    buf.Push,
		Sleep:   ctxSleep,
		OnError: func(err error) { logger.Error("capture source", "err", err) },
	})

	status := &sysStatus{start: start, cfg: cfg, store: store, buf: buf, eng: eng, sup: sup}
	expvar.Publish("zeitspiegel_dropped_frames", expvar.Func(func() any { return sup.Dropped() }))
	expvar.Publish("zeitspiegel_buffer", expvar.Func(func() any {
		st := buf.Stats()
		return map[string]any{"len": st.Len, "bytes": st.Bytes, "filled_s": st.Span.Seconds()}
	}))

	handler := httpapi.New(httpapi.Deps{
		Logger:        logger,
		Status:        status,
		Delay:         eng,
		Clip:          clipper,
		Config:        store,
		Frames:        buf,
		DelayedFrames: &delayedFrames{buf: buf, eng: eng},
		Ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		UI:      web.Handler(),
		Healthy: func() bool { return !sup.Degraded() },
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Bind, Handler: handler}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(1)
	go func() { // control plane
		defer wg.Done()
		logger.Info("http listening", "addr", cfg.Bind)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()
	wg.Add(1)
	go func() { // capture (sole buffer writer)
		defer wg.Done()
		if err := sup.Run(ctx); err != nil {
			errCh <- fmt.Errorf("capture: %w", err)
		}
	}()
	wg.Add(1)
	go func() { // shutdown coordinator
		defer wg.Done()
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	// Render loop on the main goroutine (SDL needs the main thread; the
	// sdl-tagged file locks it in init). Headless builds idle here.
	var runErr error
	if display != nil {
		logger.Info("display loop starting", "fps", cfg.FPS(), "mirror", cfg.MirrorFlip)
		tick := time.NewTicker(time.Duration(float64(time.Second) / cfg.FPS()))
		defer tick.Stop()
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case err := <-errCh:
				runErr = err
				stop()
				break loop
			case <-tick.C:
				if sel := eng.Tick(time.Now()); sel.Render {
					if err := display.Render(sel.Frame); err != nil {
						logger.Error("render", "seq", sel.Frame.Seq, "err", err)
					}
				}
			}
		}
	} else {
		logger.Info("headless (no sdl build tag): preview via web UI", "addr", cfg.Bind)
		select {
		case <-ctx.Done():
		case err := <-errCh:
			runErr = err
			stop()
		}
	}

	wg.Wait()
	select {
	case err := <-errCh:
		if runErr == nil {
			runErr = err
		}
	default:
	}
	logger.Info("shut down", "uptime", time.Since(start).Round(time.Second))
	return runErr
}

// ctxSleep is the injected reconnect pause (capture itself is clock-free).
func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// restartable makes the supervisor reopen the source after config changes
// that need a pipeline restart (profile, camera controls): ReadFrame fails
// once, the supervisor closes and reopens with the new runtime config.
type restartable struct {
	capture.Source
	restart *atomic.Bool
}

func (r *restartable) ReadFrame(ctx context.Context) (frame.Frame, error) {
	if r.restart.CompareAndSwap(true, false) {
		return frame.Frame{}, errors.New("config changed: pipeline restart")
	}
	return r.Source.ReadFrame(ctx)
}

// delayedFrames serves /api/v1/preview?view=delayed: the frame the mirror
// shows right now (now − delay), with the same warm-up fallback as the
// display (FR-10). Read-only on the buffer; the engine's tick state is not
// touched.
type delayedFrames struct {
	buf *ringbuf.Buffer
	eng *engine.Engine
}

func (d *delayedFrames) Newest() (frame.Frame, error) {
	f, err := d.buf.At(time.Now().Add(-d.eng.Delay()))
	if errors.Is(err, ringbuf.ErrTooEarly) {
		return d.buf.Oldest()
	}
	return f, err
}

// meteredClipper records export wall time in expvar (NFR-8).
type meteredClipper struct {
	inner httpapi.ClipExporter
	gauge *expvar.Float
}

func (m *meteredClipper) ExportClip(ctx context.Context, n time.Duration, format string) (httpapi.Clip, error) {
	t0 := time.Now()
	c, err := m.inner.ExportClip(ctx, n, format)
	if err == nil {
		m.gauge.Set(time.Since(t0).Seconds())
	}
	return c, err
}
