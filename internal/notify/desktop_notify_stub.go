//go:build !darwin

package notify

func EnableDesktop(b bool)           {}
func PushDesktop(title, body string) {}
