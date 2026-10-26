package supervisor

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/Darkhand81/anona-ha-bridge/internal/media"
)

// serveHTTP exposes health and talkback. Talkback is per-camera:
//
//	POST /talk/<path>   play audio on that camera's speaker
//	POST /talk          shortcut when exactly one camera is live
//
// The body may be any ffmpeg-decodable audio; it is transcoded to Ogg/Opus with
// one 20 ms packet per page (so each RTP payload is a single valid Opus frame)
// and streamed to the camera.
func (b *Bridge) serveHTTP(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/talk", b.handleTalk)
	mux.HandleFunc("/talk/", b.handleTalk)
	fmt.Printf("[http] talkback on POST http://host%s/talk/<camera>\n", addr)
	http.ListenAndServe(addr, mux)
}

func (b *Bridge) handleTalk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST audio bytes", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/talk")
	path = strings.Trim(path, "/")

	var sess *media.Session
	if path == "" {
		// no camera named: only valid when exactly one session is live
		b.mu.RLock()
		if len(b.sessions) == 1 {
			for _, s := range b.sessions {
				sess = s
			}
		}
		b.mu.RUnlock()
		if sess == nil {
			http.Error(w, "name a camera: POST /talk/<camera>", http.StatusBadRequest)
			return
		}
	} else if sess = b.session(path); sess == nil {
		http.Error(w, "no live session for "+path, http.StatusNotFound)
		return
	}

	cmd := exec.Command("ffmpeg", "-nostdin", "-i", "pipe:0",
		"-ar", "48000", "-ac", "1", "-c:a", "libopus", "-b:a", "24k",
		"-page_duration", "20000", "-f", "ogg", "pipe:1")
	cmd.Stdin = r.Body
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	talkErr := sess.Talk(out)
	cmd.Wait()
	if talkErr != nil {
		http.Error(w, talkErr.Error(), 500)
		return
	}
	w.Write([]byte("ok"))
}
