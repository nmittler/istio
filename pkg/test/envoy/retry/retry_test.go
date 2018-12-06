package retry

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"istio.io/istio/pkg/test/util/reserveport"
	"net/http"
	"os"
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
	numBackends := 2
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

	closeAfter(backends[0], time.Second*1)

	requestURL := fmt.Sprintf("http://127.0.0.1:%d", frontend.servicePort)
	responses := sendTraffic(requestURL, time.Second * 5)

	totals := make(map[int]int)

	currStatus := 0
	currCount := 0
	currTime := time.Time{}
	for _, r := range responses {
		totals[r.statusCode] = totals[r.statusCode] + 1

		if r.statusCode != currStatus {
			if currStatus > 0 {
				fmt.Printf("End %d at %s, count: %d\n", currStatus, currTime.String(), currCount)
			}
			fmt.Printf("Begin %d at %s\n", r.statusCode, r.time.String())

			currStatus = r.statusCode
			currCount = 1
			currTime = r.time
			continue
		} else {
			currCount++
		}
	}

	if currStatus > 0 {
		fmt.Printf("End %d at %s, count: %d\n", currStatus, currTime.String(), currCount)
	}

	fmt.Println("Totals: ", totals)

	fmt.Println("app1 count: ", backends[0].appServer.count)
	fmt.Println("app2 count: ", backends[1].appServer.count)

}

type response struct {
	statusCode int
	headers    http.Header
	time       time.Time
	err        error
}

func toResponse(r *http.Response, err error) *response {
	resp := &response{
		time: time.Now(),
		err:  err,
	}
	if r != nil {
		resp.statusCode = r.StatusCode
		resp.headers = r.Header
	}
	return resp
}

func sendTraffic(requestURL string, duration time.Duration) []*response {
	responses := make([]*response, 0)
	timer := time.NewTimer(duration)
	for {
		select {
		case <-timer.C:
			return responses
		default:
			client := &http.Client{}
			req, _ := http.NewRequest("GET", requestURL, nil)
			req.Header.Set("x-envoy-retry-on", "gateway-error")
			req.Header.Set("x-envoy-max-retries", "10")
			responses = append(responses, toResponse(client.Do(req)))
		}
	}
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
