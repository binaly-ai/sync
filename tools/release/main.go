// Command release writes a release's manifest and signs it. The release workflow runs it after
// building the executables; it can also make a new signing key pair.
//
//	go run ./tools/release -dir dist -version v0.1.0   (key from UPDATE_SIGNING_KEY)
//	go run ./tools/release -genkey private.key          (prints the public key)
//
// The private key is base64 of the 32-byte ed25519 seed. It belongs in the repository's
// GitHub Actions secrets and nowhere in the tree.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/binaly-ai/sync/internal/update"
)

func main() {
	dir := flag.String("dir", "dist", "folder holding the built executables")
	version := flag.String("version", "", "release version, e.g. v0.1.0")
	genkey := flag.String("genkey", "", "write a new private key to this file and print the public key")
	flag.Parse()
	log.SetFlags(0)

	if *genkey != "" {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*genkey, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return
	}

	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		log.Fatal("UPDATE_SIGNING_KEY must be base64 of a 32-byte ed25519 seed")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if *version == "" {
		log.Fatal("-version is required")
	}

	m := update.Manifest{Version: *version, Files: map[string]string{}}
	exes, _ := filepath.Glob(filepath.Join(*dir, "*.exe"))
	if len(exes) == 0 {
		log.Fatalf("no executables in %s", *dir)
	}
	var sums []string
	for _, p := range exes {
		f, err := os.Open(p)
		if err != nil {
			log.Fatal(err)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			log.Fatal(err)
		}
		name := filepath.Base(p)
		m.Files[name] = hex.EncodeToString(h.Sum(nil))
		sums = append(sums, m.Files[name]+"  "+name)
	}
	sort.Strings(sums)
	manifest, _ := json.MarshalIndent(m, "", "  ")
	sig := ed25519.Sign(priv, manifest)

	// The program verifies with the key built into it; refuse to publish a release it would reject.
	if _, err := update.Verify(publicKey(), manifest, []byte(base64.StdEncoding.EncodeToString(sig))); err != nil {
		log.Fatal("UPDATE_SIGNING_KEY doesn't match update.PublicKey: ", err)
	}

	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	write("release.json", manifest)
	write("release.json.sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"))
	write("checksums.txt", []byte(strings.Join(sums, "\n")+"\n"))
	fmt.Printf("signed %s: %d executables\n", *version, len(exes))
}

func publicKey() ed25519.PublicKey {
	k, _ := base64.StdEncoding.DecodeString(update.PublicKey)
	return k
}
