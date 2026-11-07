//go:build darwin

package main

import "github.com/ebitengine/purego/objc"

const supportsDictation = true

// MyGo's surface is an NSTextInputClient. AppKit owns the speech session
// and delivers the result through the same text input path as keyboard IMEs.
func startSystemDictation() bool {
	app := objc.Send[objc.ID](objc.ID(objc.GetClass("NSApplication")), objc.RegisterName("sharedApplication"))
	return objc.Send[bool](app, objc.RegisterName("sendAction:to:from:"),
		objc.RegisterName("startDictation:"), objc.ID(0), objc.ID(0))
}
