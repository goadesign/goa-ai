package testscenarios

import (
    . "goa.design/goa-ai/dsl"
    . "goa.design/goa/v3/dsl"
)

// DeepNestedValidations defines nested user types with validations at each level.
func DeepNestedValidations() func() {
    return func() {
        API("alpha", func() {})

        var Level3 = Type("Level3", func() {
            Description("Level 3 leaf")
            Attribute("leaf", String, "Leaf value")
            Required("leaf")
        })

        var Level2 = Type("Level2", func() {
            Description("Level 2 node")
            Attribute("mid", String, "Middle value")
            Attribute("child", Level3, "Child L3")
            Required("mid", "child")
        })

        var Level1 = Type("Level1", func() {
            Description("Level 1 root")
            Attribute("root", String, "Root value")
            Attribute("child", Level2, "Child L2")
            Required("root", "child")
        })

        Service("alpha", func() {
            Agent("scribe", "Deep nested validator test", func() {
                Uses(func() {
                    Toolset("deep", func() {
                        Tool("validate", "Validate nested payload", func() {
                            Args(Level1)
                            Return(Level1)
                        })
                    })
                })
            })
        })
    }
}



