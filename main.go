package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Updated struct to match the actual API response format
type BitriseLogResponse struct {
	LogChunks           []LogChunk `json:"log_chunks"`
	NextBeforeTimestamp string     `json:"next_before_timestamp"`
	NextAfterTimestamp  string     `json:"next_after_timestamp"`
	IsArchived          bool       `json:"is_archived"`
	ExpiringRawLogURL   string     `json:"expiring_raw_log_url,omitempty"`
}

type LogChunk struct {
	Chunk    string `json:"chunk"`
	Position int    `json:"position"`
}

type BitriseYAMLResponse struct {
	Data string `json:"data"`
}

func main() {
	// Define command-line flags
	token := os.Getenv("BITRISE_API_TOKEN")
	appSlug := os.Getenv("BITRISE_APP_SLUG")
	buildSlug := os.Getenv("BITRISE_BUILD_SLUG")
	interval, _ := strconv.Atoi(os.Getenv("interval"))
	outputFile := os.Getenv("output_file")
	flag.Parse()

	targetLogMessage := "AI STOPS HERE WITH THE LOGS"
	fmt.Printf(targetLogMessage)
	fmt.Printf("Token is %s\n", token)
	fmt.Printf("App slug is %s\n", appSlug)
	fmt.Printf("Build slug is %s\n", buildSlug)
	fmt.Printf("Interval is %d\n", interval)
	fmt.Printf("Output file is %s\n", outputFile)

	// Set up output destination
	if outputFile != "" {
		file, err := os.Create(outputFile)
		if err != nil {
			fmt.Printf("Error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer file.Close()
	}

	// Initialize position for log fetching
	position := 0
	foundTargetMessage := false
	isFinished := false

	fmt.Printf("Starting to fetch Bitrise build logs...")
	fmt.Printf("App: %s, Build: %s\n\n", appSlug, buildSlug)

	// Continue fetching logs until the build is finished
	for {
		logResponse, err := fetchLogChunk(token, appSlug, buildSlug, position)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching logs: %v\n", err)
			os.Exit(1)
		}

		// Process each log chunk
		if len(logResponse.LogChunks) > 0 {
			for _, chunk := range logResponse.LogChunks {
				if chunk.Chunk != "" {
					appendChunksToFile(outputFile, []string{chunk.Chunk})
				}

				// Update the last position to the highest position we've seen
				if chunk.Position > position {
					position = chunk.Position
				}

				if strings.Contains(chunk.Chunk, targetLogMessage) {
					// Just found the target
					foundTargetMessage = true
					fmt.Println("\nFound target message. Collecting a few more lines...")
				}
			}
		}
		// If the log is archived, we can consider it finished
		isFinished = logResponse.IsArchived

		// If build is finished, exit the loop
		if isFinished || foundTargetMessage {
			fmt.Printf("\nLog collection finished.")
			break
		}

		// Wait before polling again
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func fetchLogChunk(token, appSlug, buildSlug string, position int) (BitriseLogResponse, error) {
	url := fmt.Sprintf("https://api.bitrise.io/v0.1/apps/%s/builds/%s/log", appSlug, buildSlug)

	// Add position parameter if not starting from the beginning
	if position > 0 {
		url = fmt.Sprintf("%s?from=%d", url, position)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return BitriseLogResponse{}, err
	}

	// Add authorization header
	req.Header.Add("Authorization", "token "+token)

	// Make the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return BitriseLogResponse{}, err
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return BitriseLogResponse{}, fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	// Parse the response
	var logChunk BitriseLogResponse
	err = json.NewDecoder(resp.Body).Decode(&logChunk)
	if err != nil {
		return BitriseLogResponse{}, err
	}

	return logChunk, nil
}

func appendChunksToFile(filePath string, chunks []string) error {
	// Open file for appending (create if doesn't exist)
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Write each chunk
	for _, chunk := range chunks {
		if _, err := file.WriteString(chunk); err != nil {
			return err
		}
	}

	return nil
}

func fetchBitriseYAML(token, appSlug string) (string, error) {
	url := fmt.Sprintf("https://api.bitrise.io/v0.1/apps/%s/bitrise.yml", appSlug)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("Authorization", "token "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	// Read response body - API returns plain YAML, not JSON-wrapped
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(bodyBytes), nil
}

func saveWorkflowContext(outputDir, token, appSlug string) error {
	yamlContent, err := fetchBitriseYAML(token, appSlug)
	if err != nil {
		return fmt.Errorf("failed to fetch Bitrise YAML: %v", err)
	}

	yamlFile := fmt.Sprintf("%s/bitrise.yml", outputDir)
	err = os.WriteFile(yamlFile, []byte(yamlContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to save YAML file: %v", err)
	}

	fmt.Printf("Saved workflow context to %s\n", yamlFile)
	return nil
}

func optimizeLogsForAnalysis(logs string) string {
	failedStepTitle := os.Getenv("BITRISE_FAILED_STEP_TITLE")
	focusFailedStepOnly := os.Getenv("analyze_log_of_failed_step_only")
	
	var optimized string
	
	// Step 1: Decide what logs to analyze (failed step vs full logs)
	if failedStepTitle != "" && focusFailedStepOnly == "true" {
		fmt.Printf("Focusing analysis on failed step: %s\n", failedStepTitle)
		optimized = extractFailedStepLogs(logs, failedStepTitle)
	} else {
		// Use full logs
		optimized = logs
	}
	
	// Step 2: Apply step-specific filtering patterns (auto-detect from logs)
	optimized = applyStepSpecificFiltering(optimized)
	
	return optimized
}

func addFailedStepErrorContext(logs, errorMessage string) string {
	// Add the error message at the beginning as important context
	contextHeader := fmt.Sprintf("=== FAILED STEP ERROR MESSAGE ===\n%s\n=== END ERROR MESSAGE ===\n\n", errorMessage)
	return contextHeader + logs
}

func extractFailedStepLogs(logs, stepTitle string) string {
	steps := parseLogsIntoSteps(logs)
	
	// Find the failed step by title
	for _, step := range steps {
		if strings.Contains(strings.ToLower(step.Title), strings.ToLower(stepTitle)) {
			return step.Logs
		}
	}
	
	// Fallback: return original logs if step not found
	return logs
}


func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func applyStepSpecificFiltering(logs string) string {
	// Always parse logs into steps first (and add error message to failed step)
	steps := parseLogsIntoSteps(logs)
	
	patternsEnabled := os.Getenv("step_log_filter_patterns_enabled")
	if patternsEnabled != "true" {
		// Patterns disabled, just reconstruct and return logs without filtering
		return reconstructLogsFromSteps(steps)
	}
	
	patterns := os.Getenv("step_log_filter_patterns")
	if patterns == "" {
		// Configuration issue - filtering enabled but no patterns defined
		fmt.Println("Warning: step_log_filter_patterns_enabled is true but step_log_filter_patterns is empty. Returning logs without filtering.")
		return reconstructLogsFromSteps(steps)
	}
	
	var filteredResults []string
	for _, step := range steps {
		stepType := detectStepTypeFromTitle(step.Title, patterns)
		
		if stepType != "" {
			fmt.Printf("Step '%s' detected as type '%s', applying filtering\n", step.Title, stepType)
			filtered := filterStepLogsByPatterns(step.Logs, stepType, patterns)
			filteredResults = append(filteredResults, filtered)
		} else {
			fmt.Printf("Step '%s' has no specific patterns, including all logs\n", step.Title)
			filteredResults = append(filteredResults, step.Logs)
		}
	}
	
	return strings.Join(filteredResults, "\n\n")
}

type StepLogs struct {
	Title string
	Logs  string
}

func parseLogsIntoSteps(logs string) []StepLogs {
	lines := strings.Split(logs, "\n")
	var steps []StepLogs
	var currentStep *StepLogs
	
	for _, line := range lines {
		// Look for step boundary markers like "| (0) Git Clone Repository |"
		if strings.Contains(line, "+----") && strings.Contains(line, "|") {
			// Check if this is a step title line (contains step number)
			if strings.Contains(line, ") ") {
				// Save previous step if exists
				if currentStep != nil {
					steps = append(steps, *currentStep)
				}
				
				// Extract step title
				stepTitle := extractStepTitle(line)
				currentStep = &StepLogs{
					Title: stepTitle,
					Logs:  line + "\n",
				}
			} else if currentStep != nil {
				// This is a step boundary but not the title, add to current step
				currentStep.Logs += line + "\n"
			}
		} else if currentStep != nil {
			// Regular log line, add to current step
			currentStep.Logs += line + "\n"
		}
	}
	
	// Add the last step
	if currentStep != nil {
		steps = append(steps, *currentStep)
	}
	
	// Add failed step error message to the appropriate step
	steps = addFailedStepErrorToSteps(steps)
	
	return steps
}

func addFailedStepErrorToSteps(steps []StepLogs) []StepLogs {
	failedStepTitle := os.Getenv("BITRISE_FAILED_STEP_TITLE")
	failedStepError := os.Getenv("BITRISE_FAILED_STEP_ERROR_MESSAGE")
	
	if failedStepTitle == "" || failedStepError == "" {
		return steps
	}
	
	// Find the failed step and add error message
	for i, step := range steps {
		if strings.Contains(strings.ToLower(step.Title), strings.ToLower(failedStepTitle)) {
			fmt.Printf("Adding error message to failed step: %s\n", step.Title)
			steps[i].Logs = addFailedStepErrorContext(step.Logs, failedStepError)
			break
		}
	}
	
	return steps
}

func reconstructLogsFromSteps(steps []StepLogs) string {
	var result []string
	for _, step := range steps {
		result = append(result, step.Logs)
	}
	return strings.Join(result, "\n")
}

func extractStepTitle(line string) string {
	// Extract step title from line like "| (0) Git Clone Repository                |"
	parts := strings.Split(line, "|")
	if len(parts) >= 2 {
		titlePart := strings.TrimSpace(parts[1])
		// Remove the step number part like "(0) "
		if idx := strings.Index(titlePart, ") "); idx != -1 && idx < 10 {
			return strings.TrimSpace(titlePart[idx+2:])
		}
		return titlePart
	}
	return ""
}

func detectStepTypeFromTitle(stepTitle, patterns string) string {
	if stepTitle == "" {
		return ""
	}
	
	stepLower := strings.ToLower(stepTitle)
	lines := strings.Split(patterns, "\n")
	
	for _, line := range lines {
		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			stepType := strings.TrimSpace(parts[0])
			
			// Check if step title contains this type
			if strings.Contains(stepLower, stepType) {
				return stepType
			}
		}
	}
	
	return ""
}

func filterStepLogsByPatterns(stepLogs, stepType, allPatterns string) string {
	// Extract keywords for this step type
	lines := strings.Split(allPatterns, "\n")
	var keywords []string
	
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), stepType+":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				keywordStr := strings.TrimSpace(parts[1])
				keywords = strings.Split(keywordStr, ",")
				// Trim whitespace from each keyword
				for i, keyword := range keywords {
					keywords[i] = strings.TrimSpace(keyword)
				}
				break
			}
		}
	}
	
	if len(keywords) == 0 {
		return stepLogs
	}
	
	// Apply filtering with these keywords
	logLines := strings.Split(stepLogs, "\n")
	var filtered []string
	
	for i, line := range logLines {
		for _, keyword := range keywords {
			if keyword != "" && strings.Contains(line, keyword) {
				// Include context around matching lines
				start := maxInt(0, i-2)
				end := minInt(len(logLines), i+4)
				
				for j := start; j < end; j++ {
					if !containsString(filtered, logLines[j]) {
						filtered = append(filtered, logLines[j])
					}
				}
				break
			}
		}
	}
	
	if len(filtered) > 0 {
		return strings.Join(filtered, "\n")
	}
	
	// If no keywords matched, return original step logs
	return stepLogs
}
