package testscenarios

import (
	aidsl "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// MethodComplexEmbedded defines a method-bound tool with nested/embedded user types.
func MethodComplexEmbedded() func() {
	return func() {
		API("alpha", func() {})

		var Address = Type("Address", func() {
			Description("Postal address")
			Attribute("street", String, "Street")
			Attribute("city", String, "City")
			Required("street", "city")
		})

		var Profile = Type("Profile", func() {
			Description("User profile")
			Attribute("id", String, "Identifier")
			Attribute("name", String, "Name")
			Attribute("address", Address, "Address")
			Required("id", "address")
		})

		var UpsertArgs = Type("UpsertArgs", func() {
			Attribute("profile", Profile, "Profile")
			Required("profile")
		})

		Service("alpha", func() {
			Method("UpsertProfile", func() {
				Payload(Profile)
				Result(Profile)
			})
			aidsl.Agent("scribe", "Profile helper", func() {
				aidsl.Use("profiles", func() {
					aidsl.Tool("upsert", "Upsert a profile", func() {
						aidsl.Args(UpsertArgs)
						aidsl.Return(Profile)
						aidsl.BindTo("alpha", "UpsertProfile")
					})
				})
			})
		})
	}
}
