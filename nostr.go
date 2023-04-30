package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
)

type Tag []string
type Tags []Tag
type NostrEvent struct {
	ID        string    `json:"id"`
	PubKey    string    `json:"pubkey"`
	CreatedAt time.Time `json:"created_at"`
	Kind      int       `json:"kind"`
	Tags      Tags      `json:"tags"`
	Content   string    `json:"content"`
	Sig       string    `json:"sig"`
}

var nip57Receipt nostr.Event
var zapEventSerializedStr string
var nip57ReceiptRelays []string

func Nip57DescriptionHash(zapEventSerialized string) string {
	hash := sha256.Sum256([]byte(zapEventSerialized))
	hashString := hex.EncodeToString(hash[:])
	return hashString
}

func DecodeBench32(key string) string {
	if _, v, err := nip19.Decode(key); err == nil {
		return v.(string)
	}
	return key

}

func EncodeBench32Public(key string) string {
	if v, err := nip19.EncodePublicKey(key); err == nil {
		return v
	}
	return key
}

func EncodeBench32Private(key string) string {
	if v, err := nip19.EncodePrivateKey(key); err == nil {
		return v
	}
	return key
}

func EncodeBench32Note(key string) string {
	if v, err := nip19.EncodeNote(key); err == nil {
		return v
	}
	return key
}

func sendMessage(receiverKey string, message string) {

	var relays []string
	var tags nostr.Tags
	reckey := DecodeBench32(receiverKey)
	tags = append(tags, nostr.Tag{"p", reckey})

	//references, err := optSlice(opts, "--reference")
	//if err != nil {
	//	return
	//}
	//for _, ref := range references {
	//tags = append(tags, nostr.Tag{"e", reckey})
	//}

	// parse and encrypt content
	privkeyhex := DecodeBench32(s.NostrPrivateKey)
	pubkey, _ := nostr.GetPublicKey(privkeyhex)

	sharedSecret, err := nip04.ComputeSharedSecret(reckey, privkeyhex)
	if err != nil {
		log.Printf("Error computing shared key: %s. x\n", err.Error())
		return
	}

	encryptedMessage, err := nip04.Encrypt(message, sharedSecret)
	if err != nil {
		log.Printf("Error encrypting message: %s. \n", err.Error())
		return
	}

	event := nostr.Event{
		PubKey:    pubkey,
		CreatedAt: time.Now(),
		Kind:      nostr.KindEncryptedDirectMessage,
		Tags:      tags,
		Content:   encryptedMessage,
	}
	event.Sign(privkeyhex)
	publishNostrEvent(event, relays)
	log.Printf("%+v\n", event)
}

func handleNip05(w http.ResponseWriter, r *http.Request) {
	var err error
	var response string

	var allusers []Params
	allusers, err = GetAllUsers(s.Domain)
	firstpartstring := "{\n  \"names\": {\n"
	finalpartstring := " \t}\n}"
	var middlestring = ""

	for _, user := range allusers {
		nostrnpubHex := DecodeBench32(user.Npub)
		if user.Npub != "" { //do some more validation checks
			middlestring = middlestring + "\t\"" + user.Name + "\"" + ": " + "\"" + nostrnpubHex + "\"" + ",\n"
		}
	}

	if s.Nip05 {
		//Remove ',' from last entry
		if len(middlestring) > 2 {
			middlestringtrim := middlestring[:len(middlestring)-2]
			middlestringtrim += "\n"

			response = firstpartstring + middlestringtrim + finalpartstring
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintf(w, response)
	} else {
		return
	}

	if err != nil {
		return
	}
}

func GetNostrProfileMetaData(npub string, index int) (nostr.ProfileMetadata, error) {
	ctx, _ := context.WithTimeout(context.Background(), 3*time.Second)

	var metadata *nostr.ProfileMetadata
	// connect to first relay, todo, check on all/for errors

	if index < len(Relays) {
		rel := Relays[index]
		log.Printf("Get Image from: %s", rel)
		url := rel
		relay, err := nostr.RelayConnect(ctx, url)
		if err != nil {
			log.Printf("Could not get Connect, trying next relay")
			return GetNostrProfileMetaData(npub, index+1)
			//return *metadata, err
		}

		// create filters
		var filters nostr.Filters
		if _, v, err := nip19.Decode(npub); err == nil {
			t := make(map[string][]string)
			t["p"] = []string{v.(string)}
			filters = []nostr.Filter{{
				Authors: []string{v.(string)},
				Kinds:   []int{0},
				// limit = 3, get the three most recent notes
				Limit: 1,
			}}
		} else {
			log.Printf("Could not find Profile, trying next relay")
			return GetNostrProfileMetaData(npub, index+1)
			//return *metadata, err

		}
		sub, err := relay.Subscribe(ctx, filters)
		evs := make([]nostr.Event, 0)

		go func() {
			<-sub.EndOfStoredEvents

		}()

		for ev := range sub.Events {

			evs = append(evs, *ev)
		}
		relay.Close()

		if len(evs) > 0 {
			metadata, err = nostr.ParseMetadata(evs[0])
			log.Printf("Success getting Nostr Profile")
		} else {
			err = fmt.Errorf("no profile found for npub %s on relay %s", npub, url)
			log.Printf("Could not find Profile, trying next relay")
			return GetNostrProfileMetaData(npub, index+1)
		}

		return *metadata, err
	} else {
		return *metadata, fmt.Errorf("Couldn't download Profile for given relays")

	}

}

func publishNostrEvent(ev nostr.Event, relays []string) {
	// Add more relays, remove trailing slashes, and ensure unique relays
	relays = uniqueSlice(cleanUrls(append(relays, Relays...)))

	ev.Sign(s.NostrPrivateKey)

	var wg sync.WaitGroup
	wg.Add(len(relays))

	// Create a buffered channel to control the number of active goroutines
	concurrencyLimit := 20
	goroutines := make(chan struct{}, concurrencyLimit)

	// Publish the event to relays
	for _, url := range relays {
		goroutines <- struct{}{}
		go func(url string) {
			defer func() {
				<-goroutines
				wg.Done()
			}()

			var err error
			var conn *nostr.Relay
			var status nostr.Status
			maxRetries := 3
			retryDelay := 1 * time.Second

			for i := 0; i < maxRetries; i++ {
				// Set a timeout for connecting to the relay
				connCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				conn, err = nostr.RelayConnect(connCtx, url)
				cancel()

				if err != nil {
					log.Printf("Error connecting to relay %s: %v", url, err)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				}
				defer conn.Close()

				// Set a timeout for publishing to the relay
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				status, err = conn.Publish(pubCtx, ev)
				cancel()

				if err != nil {
					log.Printf("Error publishing to relay %s: %v", url, err)
					time.Sleep(retryDelay)
					retryDelay *= 2
					continue
				} else {
					log.Printf("[NOSTR] published to %s: %s", url, status.String())
					break
				}
			}
		}(url)
	}

	wg.Wait()
}

func ExtractNostrRelays(zapEvent nostr.Event) []string {
	relaysTag := zapEvent.Tags.GetFirst([]string{"relays"})
	log.Printf("Zap relaysTag: %s", relaysTag)

	if relaysTag == nil || len(*relaysTag) == 0 {
		return []string{}
	}

	// Skip the first element, which is the tag name
	relays := (*relaysTag)[1:]
	log.Printf("Zap relays: %v", relays)

	return relays
}

func CreateNostrReceipt(zapEvent nostr.Event, invoice string) (nostr.Event, error) {
	pub, err := nostr.GetPublicKey(nostrPrivkeyHex)
	if err != nil {
		return nostr.Event{}, err
	}

	zapEventSerialized, err := json.Marshal(zapEvent)
	if err != nil {
		return nostr.Event{}, err
	}

	nip57Receipt := nostr.Event{
		PubKey:    pub,
		CreatedAt: time.Now(),
		Kind:      9735,
		Tags: nostr.Tags{
			*zapEvent.Tags.GetFirst([]string{"p"}),
			[]string{"bolt11", invoice},
			[]string{"description", string(zapEventSerialized)},
		},
	}

	if eTag := zapEvent.Tags.GetFirst([]string{"e"}); eTag != nil {
		nip57Receipt.Tags = nip57Receipt.Tags.AppendUnique(*eTag)
	}

	err = nip57Receipt.Sign(nostrPrivkeyHex)
	if err != nil {
		return nostr.Event{}, err
	}

	return nip57Receipt, nil
}

func uniqueSlice(slice []string) []string {
	keys := make(map[string]bool)
	list := make([]string, 0, len(slice))
	for _, entry := range slice {
		if _, exists := keys[entry]; !exists && entry != "" {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

func cleanUrls(slice []string) []string {
	list := make([]string, 0, len(slice))
	for _, entry := range slice {
		if strings.HasSuffix(entry, "/") {
			entry = entry[:len(entry)-1]
		}
		list = append(list, entry)
	}
	return list
}
