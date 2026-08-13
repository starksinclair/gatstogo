package attendant

import "net/http"

func Test(w http.ResponseWriter, r *http.Request) {
	_, err := w.Write([]byte("Hello World! to this attendant dashboard in this project"))
	if err != nil {
		return
	}
}
