---

## 10. 📜 The Golden Rules: Working with Codegen

* ✍️ **Design First:** Always make changes in your `design/*.go` files.
* 🔄 **Regenerate:** Run `goa gen <module>/design` to apply your changes.
* 🚫 **Hands Off `gen/`:** Never edit the `gen/` directory by hand. Your changes will be overwritten!

