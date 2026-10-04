package generate

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/generationbudget"
)

func TestSpriteConfiguredWorkersAndOrder(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {4, 3}, {8, 8}, {64, 64}} {
		for _, class := range []generationbudget.Class{generationbudget.CPU, generationbudget.GPU} {
			t.Run(fmt.Sprintf("total=%d/gpu=%d/class=%d", limits[0], limits[1], class), func(t *testing.T) {
				budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: limits[0], MaxGPUProcesses: limits[1]})
				if err != nil {
					t.Fatal(err)
				}
				g := Generator{Budget: budget}
				workers := g.spriteWorkers(class, 81)
				want := limits[0]
				if class == generationbudget.GPU {
					want = limits[1]
				}
				if workers != want || g.spriteWorkers(class, 2) != min(want, 2) {
					t.Fatalf("workers=%d want=%d", workers, want)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				started := make(chan int, 81)
				finished := make(chan int, 81)
				gates := make([]chan struct{}, 81)
				tiles := make([]image.Image, 81)
				for i := range gates {
					gates[i] = make(chan struct{})
					tile := image.NewNRGBA(image.Rect(0, 0, 160, 90))
					tile.SetNRGBA(0, 0, color.NRGBA{R: uint8(i), A: 255})
					tiles[i] = tile
				}
				times := make([]float64, 81)
				for i := range times {
					times[i] = 2.5 + float64(i)*0.125
				}
				done := make(chan error, 1)
				var images []image.Image
				go func() {
					var captureErr error
					images, captureErr = captureSpriteSequence(ctx, times, workers, 160, 0, func(ctx context.Context, at float64) (image.Image, error) {
						i := int((at - 2.5) / 0.125)
						started <- i
						select {
						case <-gates[i]:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						finished <- i
						return tiles[i], nil
					})
					done <- captureErr
				}()
				for offset := 0; offset < 81; offset += workers {
					wave := make([]int, min(workers, 81-offset))
					for i := range wave {
						select {
						case wave[i] = <-started:
						case <-ctx.Done():
							t.Fatal("configured concurrency was not reached", ctx.Err())
						}
					}
					// Complete each wave in reverse start order.
					for i := len(wave) - 1; i >= 0; i-- {
						close(gates[wave[i]])
						select {
						case <-finished:
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				}
				if err := <-done; err != nil || len(images) != 81 {
					t.Fatalf("tiles=%d err=%v", len(images), err)
				}
				for i := range images {
					if images[i] != tiles[i] {
						t.Fatalf("tile %d lost requested timestamp/order", i)
					}
				}
			})
		}
	}
	if (Generator{}).spriteWorkers(generationbudget.CPU, 81) != 1 {
		t.Fatal("ordinary unbudgeted CPU extraction changed")
	}
}

func TestSpriteFailureAndCancellationDrainWorkers(t *testing.T) {
	for _, mode := range []string{"failure", "nil", "dimensions", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{}, 4)
			cancelled := make(chan struct{}, 4)
			fail := make(chan struct{})
			drain := make(chan struct{})
			var drainOnce sync.Once
			defer drainOnce.Do(func() { close(drain) })
			done := make(chan error, 1)
			cause := errors.New("decode failed")
			go func() {
				images, err := captureSpriteFrames(ctx, 81, 4, 160, 90, func(ctx context.Context, i int) (image.Image, error) {
					started <- struct{}{}
					if i == 0 && mode != "cancel" {
						select {
						case <-fail:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						switch mode {
						case "failure":
							return nil, cause
						case "nil":
							return nil, nil
						default:
							return image.NewNRGBA(image.Rect(0, 0, 160, 88)), nil
						}
					}
					<-ctx.Done()
					cancelled <- struct{}{}
					<-drain // Simulate subprocess Wait/pipe cleanup after cancellation.
					return nil, ctx.Err()
				})
				if images != nil {
					err = errors.New("failed attempt returned partial images")
				}
				done <- err
			}()
			for range 4 {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if mode == "cancel" {
				cancel()
			} else {
				close(fail)
			}
			wantCancelled := 3
			if mode == "cancel" {
				wantCancelled = 4
			}
			for range wantCancelled {
				select {
				case <-cancelled:
				case <-time.After(5 * time.Second):
					t.Fatal("sibling worker was not cancelled")
				}
			}
			select {
			case err := <-done:
				t.Fatalf("returned before workers drained: %v", err)
			default:
			}
			drainOnce.Do(func() { close(drain) })
			err := <-done
			if err == nil || (mode == "failure" && !errors.Is(err, cause)) || (mode == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("failure cause lost: %v", err)
			}
		})
	}
}

func TestSpriteSheetsShareTotalAndGPUAdmission(t *testing.T) {
	budget, err := generationbudget.New(generationbudget.Settings{MaxProcesses: 6, MaxGPUProcesses: 4})
	if err != nil {
		t.Fatal(err)
	}
	g := Generator{Budget: budget}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	active, gpuActive, peak, gpuPeak := 0, 0, 0, 0
	started := make(chan struct{}, 6)
	gate := make(chan struct{})
	done := make(chan error, 3)
	run := func(class generationbudget.Class, count int) {
		args := []string{"-i", "input", "-"}
		if class == generationbudget.GPU {
			args = append([]string{"-hwaccel", "vaapi"}, args...)
		}
		_, err := captureSpriteFrames(ctx, count, g.spriteWorkers(class, count), 160, 90, func(ctx context.Context, _ int) (image.Image, error) {
			_, release, err := g.acquireGeneration(ctx, args)
			if err != nil {
				return nil, err
			}
			defer release()
			mu.Lock()
			active++
			if class == generationbudget.GPU {
				gpuActive++
			}
			peak, gpuPeak = max(peak, active), max(gpuPeak, gpuActive)
			mu.Unlock()
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-ctx.Done():
			}
			mu.Lock()
			active--
			if class == generationbudget.GPU {
				gpuActive--
			}
			mu.Unlock()
			return image.NewNRGBA(image.Rect(0, 0, 160, 90)), ctx.Err()
		})
		done <- err
	}
	waitStarted := func(count int) {
		for range count {
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("shared budget deadlocked", ctx.Err())
			}
		}
	}
	go run(generationbudget.CPU, 2)
	waitStarted(2)
	go run(generationbudget.GPU, 81)
	go run(generationbudget.GPU, 81)
	waitStarted(4)
	close(gate)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if peak != 6 || gpuPeak != 4 || active != 0 || gpuActive != 0 {
		t.Fatalf("active=%d GPU=%d peak=%d GPU peak=%d", active, gpuActive, peak, gpuPeak)
	}
}

func TestSpriteCancellationWhileWorkersWaitForAdmission(t *testing.T) {
	budget, _ := generationbudget.New(generationbudget.Settings{MaxProcesses: 4, MaxGPUProcesses: 2})
	g := Generator{Budget: budget}
	var occupied []func()
	for range 2 {
		release, err := budget.Acquire(context.Background(), generationbudget.GPU)
		if err != nil {
			t.Fatal(err)
		}
		occupied = append(occupied, release)
		defer release()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	done := make(chan error, 1)
	go func() {
		images, err := captureSpriteFrames(ctx, 81, g.spriteWorkers(generationbudget.GPU, 81), 160, 90, func(ctx context.Context, _ int) (image.Image, error) {
			started <- struct{}{}
			_, release, err := g.acquireGeneration(ctx, []string{"-hwaccel", "vaapi", "-i", "input", "-"})
			if release != nil {
				release()
			}
			return nil, err
		})
		if images != nil {
			err = errors.New("cancelled admission returned images")
		}
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting worker cancellation: %v", err)
	}
	for _, release := range occupied {
		release()
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	for range 4 {
		release, err := budget.Acquire(fresh, generationbudget.CPU)
		if err != nil {
			t.Fatal("cancelled worker retained admission", err)
		}
		defer release()
	}
}
