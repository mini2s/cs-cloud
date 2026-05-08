package platform

import "os"

const DefaultCloudBaseURL = "https://zgsm.sangfor.com"

func Getenv(key string) string {
	return os.Getenv(key)
}
