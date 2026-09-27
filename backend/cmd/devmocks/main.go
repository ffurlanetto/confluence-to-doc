// Command devmocks runs a fake Confluence (with a demo page tree) and a fake
// OpenID Connect provider, so the whole application can be run and tested
// locally without any external dependency. FOR DEVELOPMENT ONLY.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"time"

	"github.com/ffurlanetto/confluence-to-doc/backend/internal/auth/oidcmock"
	"github.com/ffurlanetto/confluence-to-doc/backend/internal/confluence/fake"
)

func main() {
	confAddr := flag.String("confluence-addr", ":8090", "listen address of the fake Confluence")
	idpAddr := flag.String("oidc-addr", ":8091", "listen address of the fake OIDC provider")
	issuer := flag.String("oidc-issuer", "http://localhost:8091", "externally visible issuer URL")
	pat := flag.String("pat", "dev-pat", "the only personal access token accepted by the fake Confluence")
	flag.Parse()

	idp := oidcmock.New("confluence-to-doc", "dev-secret", oidcmock.User{Subject: "dev-user", Email: "dev@example.com", Name: "Dev User"})
	idp.Issuer = *issuer

	conf := fake.New(*pat)
	seed(conf)

	go serve(*idpAddr, idp, "fake OIDC provider (client_id=confluence-to-doc, secret=dev-secret)")
	serve(*confAddr, conf, fmt.Sprintf("fake Confluence (PAT=%s)", *pat))
}

func serve(addr string, h http.Handler, what string) {
	log.Printf("%s listening on %s", what, addr)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func seed(f *fake.Server) {
	f.AddAttachment("100/diagram.png", demoImage())
	f.AddPage(fake.Page{ID: "100", Title: "Documentation produit", SpaceKey: "DOC", Body: `
<p>Bienvenue dans la documentation. Cette page racine contient une <strong>image</strong>, un tableau et des sous-pages.</p>
<p><img src="/download/attachments/100/diagram.png" alt="Diagramme"></p>
<table><tr><th>Version</th><th>Date</th></tr><tr><td>1.0</td><td>2026-01-15</td></tr><tr><td>2.0</td><td>2026-06-01</td></tr></table>
<p>Voir aussi le <a href="/pages/viewpage.action?pageId=121">guide de déploiement</a>.</p>`})
	f.AddPage(fake.Page{ID: "110", ParentID: "100", Title: "Guide utilisateur", SpaceKey: "DOC", Body: `
<h1>Prise en main</h1><p>Connectez-vous puis choisissez une page.</p>
<h2>Raccourcis</h2><ul><li>Ctrl+K : recherche</li><li>Ctrl+E : export</li></ul>`})
	f.AddPage(fake.Page{ID: "111", ParentID: "110", Title: "FAQ", SpaceKey: "DOC", Body: `<p><em>Q :</em> Combien de temps les exports restent-ils disponibles ? <em>R :</em> 48 heures.</p>`})
	f.AddPage(fake.Page{ID: "120", ParentID: "100", Title: "Guide d'administration", SpaceKey: "DOC", Body: `<p>Configuration du serveur.</p><pre>APP_ROLE=worker</pre>`})
	f.AddPage(fake.Page{ID: "121", ParentID: "120", Title: "Déploiement", SpaceKey: "DOC", Body: `<p>Utilisez l'image Docker fournie.</p>`})
	f.AddPage(fake.Page{ID: "122", ParentID: "120", Title: "Supervision", SpaceKey: "DOC", Body: `<p>Métriques Prometheus sur <code>:9090/metrics</code>.</p>`})
	for i := 1; i <= 3; i++ {
		f.AddPage(fake.Page{ID: fmt.Sprint(1220 + i), ParentID: "122", Title: fmt.Sprintf("Alerte %d", i), SpaceKey: "DOC",
			Body: fmt.Sprintf("<p>Procédure de traitement de l'alerte %d.</p>", i)})
	}
	f.AddPage(fake.Page{ID: "200", Title: "Notes de réunion", SpaceKey: "TEAM", Body: `<p>Page isolée, sans enfants.</p>`})
}

func demoImage() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 400, 160))
	for x := range 400 {
		for y := range 160 {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / 400), G: 90, B: uint8(y * 255 / 160), A: 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
