package main

import "github.com/kncept/Emissary/mailer/awsutil"

func main() {
	if awsutil.IsAwsLambda() {
		runAsAwsLambda()
		return
	}

	panic("Must run as a lambda")
}

func runAsAwsLambda() {}
