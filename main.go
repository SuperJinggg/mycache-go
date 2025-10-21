package main

import (
	"context"
	"errors"
	"log"
	"mycache"
	"net/http"
)

var Store = map[string][]byte{
	"red":   []byte("#FF0000"),
	"green": []byte("#00FF00"),
	"blue":  []byte("#0000FF"),
}

var Group = mycache.NewGroup("colors", 64<<20, mycache.GetterFunc(
	func(ctx context.Context, key string, dest mycache.Sink) error {
		log.Println("looking up", key)
		v, ok := Store[key]
		if !ok {
			return errors.New("color not found")
		}
		dest.SetBytes(v)
		return nil
	},
))

func main() {
	http.HandleFunc("/color", func(w http.ResponseWriter, r *http.Request) {
		color := r.FormValue("name")
		var b []byte
		err := Group.Get(r.Context(), color, mycache.AllocatingByteSliceSink(&b))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
	})

	log.Println("Server starting on :8080")
	http.ListenAndServe(":8080", nil)
}
