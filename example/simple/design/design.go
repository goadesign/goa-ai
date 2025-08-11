package design

import . "goa.design/goa/v3/dsl"

var _ = API("test", func() {
    Title("Test")
})

var _ = Service("test", func() {
    Method("test", func() {
        Result(String)
    })
})
