package main

import(
	"fmt"
	"os"
	"os/user"
	"buddy/repl"
)

func main() {
	user, err := user.Current()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Yo %s! It's Buddylang!\n", user.Username)
	repl.Start(os.Stdin,os.Stdout)
}
