---
template: home.html
title: mongotest
hide:
  - navigation
  - toc
  - footer
---

<!-- The landing page is rendered by docs/overrides/home.html. The code block
     below is the only Markdown on this page: the template drops it into the
     hero so it gets the same syntax highlighting and copy button as every
     other page. -->

```go title="foo_test.go"
func TestFoo(t *testing.T) {
    // Starts mongo in a container on a free port and waits for it.
    conn, err := mongotest.NewTestConnection(true)
    if err != nil {
        t.Fatal(err)
    }
    // Removes the container when the test ends, pass or fail.
    t.Cleanup(func() { _ = conn.KillMongoContainer() })

    coll := conn.D("app").C("heroes")
    _, err = coll.Insert().One(&hero{Name: "Diana"})
    if err != nil {
        t.Fatal(err)
    }
}
```
