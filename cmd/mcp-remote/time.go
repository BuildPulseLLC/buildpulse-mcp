package main

import "time"

// unixToTime converts Unix epoch seconds (the format DynamoDB TTL
// expects) back to a time.Time. Zero input returns the zero time.
func unixToTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0).UTC()
}
