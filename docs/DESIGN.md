

This project is a golang service with a react frontend.

The cmd/frontend backend reads from S3 
Sending uses the AWS API to send emails
Metadata about read/new emails is in the 'metadata' package

The cmd/sns-receiver backend is a convinience lambda.
It will read from SNS, and write to as many S3 buckets as are defined.

The frontend is react based, and is embedded into the cmd/frontend binary


