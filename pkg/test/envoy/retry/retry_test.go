package retry

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-multierror"
	"io"
	"io/ioutil"
	"istio.io/istio/pkg/test/util/reserveport"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

func TestRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempDir, err := createTempDir()
	if err != nil {
		t.Fatal(err)
	}

	portMgr, err := reserveport.NewPortManager()
	if err != nil {
		t.Fatal(err)
	}

	// Create the backends.
	numBackends := 10
	backends := make([]*backend, 0, numBackends)
	for i := 0; i < numBackends; i++ {
		be, err := newBackend(ctx, backendConfig{
			name:    fmt.Sprintf("be%d", i+1),
			tempDir: tempDir,
			portMgr: portMgr,
		})
		if err != nil {
			t.Fatal(err)
		}
		backends = append(backends, be)
	}

	// Create the frontend
	frontend, err := newFrontend(ctx, frontendConfig{
		tempDir:  tempDir,
		portMgr:  portMgr,
		backends: backends,
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)

	for i := 0; i < 3; i++ {
		closeAfter(backends[i], time.Second*time.Duration(i+1))
	}

	requestURL := fmt.Sprintf("http://127.0.0.1:%d", frontend.servicePort)
	codes, err := sendTraffic(requestURL, time.Second * time.Duration(numBackends + 5))
	if err != nil {
		t.Fatal(err)
	}

	badCodeCount := 0
	for code, count := range codes {
		if code != 200 {
			badCodeCount += count
		}
	}

	fmt.Println("Totals: ", codes)

	for i, be := range backends {
		fmt.Printf("be[%d] count: %d\n", i, be.appServer.count)
	}

	if badCodeCount > 0 {
		t.Fatalf("received bad codes: %v", codes)
	}
}

func sendTraffic(requestURL string, duration time.Duration) (map[int]int, error) {
	numThreads := 4

	wg := sync.WaitGroup{}
	codes := make([]map[int]int, numThreads)
	errors := make([]error, numThreads)
	for i := 0; i < numThreads; i++ {
		j := i
		codes[j] = make(map[int]int)
		wg.Add(1)
		go func() {
			client := &http.Client{}
			timer := time.NewTimer(duration)
			ticker := time.NewTicker(time.Millisecond * 50)
			for {
				select {
				case <-timer.C:
					ticker.Stop()
					wg.Done()
					return
				case <-ticker.C:
					req, _ := http.NewRequest("GET", requestURL, nil)
					//req.Header.Set("x-envoy-retry-on", "gateway-error")
					//req.Header.Set("x-envoy-max-retries", "10")
					resp, err := client.Do(req)
					if err != nil {
						errors[j] = multierror.Append(errors[j], err)
					} else {
						codes[j][resp.StatusCode] = codes[j][resp.StatusCode] + 1
					}
				}
			}
		}()
	}

	wg.Wait()

	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}

	mergedCodes := make(map[int]int)
	for _, codeMap := range codes {
		for code, count := range codeMap {
			mergedCodes[code] = mergedCodes[code] + count
		}
	}
	return mergedCodes, nil
}

func closeAfter(c io.Closer, d time.Duration) {
	go func() {
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
			_ = c.Close()
		}
	}()
}

func createTempDir() (string, error) {
	tmpDir, err := ioutil.TempDir(os.TempDir(), "envoy_retry_test")
	if err != nil {
		return "", err
	}
	return tmpDir, nil
}
