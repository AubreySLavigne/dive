package image

type AnalyzerFactory func(string) Analyzer

func GetAnalyzer(imageID string) dockerImageAnalyzer {
	// todo: add ability to have multiple image formats... for the meantime only use docker
	return newDockerImageAnalyzer(imageID)
}
