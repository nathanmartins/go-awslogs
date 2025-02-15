package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"golang.org/x/time/rate"
)

var tailMode bool
var groupName string
var pollInterval time.Duration
var timeRange time.Duration

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use: "go-awslogs",
	RunE: func(cmd *cobra.Command, args []string) error {

		if groupName == "" {
			return fmt.Errorf("group name is required")
		}

		var file *os.File
		var err error
		groupNameSlug := strings.TrimPrefix(groupName, "/")
		groupNameSlug = strings.ReplaceAll(groupNameSlug, "/", "-")
		var outputFile = fmt.Sprintf("%s.txt", groupNameSlug)

		if !tailMode {
			_ = os.RemoveAll(outputFile)
			log.Printf("Saving logs to %s", outputFile)
			file, err = os.Create(outputFile)
			if err != nil {
				log.Fatalf("Failed to create output file: %v", err)
			}
			defer file.Close()
		}

		bufferedWriter := bufio.NewWriter(file)

		reader, err := NewLogReader()
		if err != nil {
			log.Fatalf("Failed to create log reader: %v", err)
		}

		ctx := context.Background()
		startTime := time.Now().Add(-timeRange)

		log.Printf("Getting logs since %s", startTime.Format("2006-01-02 15:04:05.000000000 -0700 MST"))

		streams, err := reader.getStreams(ctx, groupName)
		if err != nil {
			log.Fatalf("Failed to get streams: %v", err)
		}

		maxWorkers := 3
		streamChan := make(chan string, len(streams))
		var wg sync.WaitGroup
		logChan := make(chan *types.OutputLogEvent, 1000)

		for i := 0; i < maxWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for stream := range streamChan {
					var streamWg sync.WaitGroup
					streamWg.Add(1)
					reader.getLogsForStream(ctx, groupName, stream, startTime, logChan, &streamWg)
					streamWg.Wait()
				}
			}()
		}

		for _, stream := range streams {
			streamChan <- stream
		}
		close(streamChan)

		// Process logs based on mode
		done := make(chan bool)
		go func() {
			for event := range logChan {
				logLine := fmt.Sprintf("%s\n", *event.Message)
				if tailMode {
					fmt.Print(logLine)
				} else {
					_, err = fmt.Fprint(bufferedWriter, logLine)
					if err != nil {
						log.Printf("Error writing to file: %v", err)
					}
					err := bufferedWriter.Flush()
					if err != nil {
						log.Printf("Error flushing file: %v", err)
					}
				}
			}
			done <- true
		}()

		wg.Wait()
		if !tailMode {
			close(logChan)
			<-done
			log.Printf("Logs have been saved to %s", file.Name())
		} else {
			log.Printf("Tailing logs... (Press Ctrl+C to stop)")
			<-done
		}

		return nil
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.go-awslogs.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	rootCmd.Flags().BoolP("help", "h", false, "Help message for toggle")
	rootCmd.Flags().BoolVarP(&tailMode, "tail", "t", false, "Tail logs")
	rootCmd.Flags().StringVarP(&groupName, "group-name", "g", "", "Group name")
	rootCmd.Flags().DurationVarP(&pollInterval, "poll-interval", "i", time.Second*5, "Poll interval")
	rootCmd.Flags().DurationVarP(&timeRange, "time-range", "r", time.Minute*10, "Time range")
}

type LogReader struct {
	client      *cloudwatchlogs.Client
	rateLimiter *rate.Limiter
}

func NewLogReader() (*LogReader, error) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("unable to load SDK config: %v", err)
	}

	// Increase rate limit to 5 requests per second with burst of 10
	limiter := rate.NewLimiter(rate.Limit(5), 10)

	client := cloudwatchlogs.NewFromConfig(cfg)
	return &LogReader{
		client:      client,
		rateLimiter: limiter,
	}, nil
}

func (lr *LogReader) getStreams(ctx context.Context, groupName string) ([]string, error) {
	var streams []string
	var nextToken *string

	startTimeMs := time.Now().Add(-timeRange).UnixMilli()

	log.Printf("Fetching log streams for group %s with events after %v", groupName, time.UnixMilli(startTimeMs))

	for {
		err := lr.rateLimiter.Wait(ctx)
		if err != nil {
			return nil, fmt.Errorf("rate limiter wait failed: %v", err)
		}

		input := &cloudwatchlogs.DescribeLogStreamsInput{
			LogGroupName: &groupName,
			NextToken:    nextToken,
			OrderBy:      types.OrderByLastEventTime,
			Descending:   aws.Bool(true),
			Limit:        aws.Int32(50),
		}

		result, err := lr.client.DescribeLogStreams(ctx, input)
		if err != nil {
			if isThrottlingError(err) {
				log.Printf("Throttling while fetching streams, waiting...")
				time.Sleep(time.Second * 2)
				continue
			}
			return nil, fmt.Errorf("failed to describe log streams: %v", err)
		}

		for _, stream := range result.LogStreams {
			// Only include streams that have events in our time window
			if stream.LastEventTimestamp != nil && *stream.LastEventTimestamp >= startTimeMs {
				if stream.LogStreamName != nil {
					streams = append(streams, *stream.LogStreamName)
					log.Printf("Including stream %s (last event: %v)", *stream.LogStreamName,
						time.UnixMilli(*stream.LastEventTimestamp).Format(time.RFC3339))
				}
			} else {
				if stream.LogStreamName != nil {
					log.Printf("Skipping stream %s (last event: %v)", *stream.LogStreamName,
						formatTimestamp(stream.LastEventTimestamp))
				}
			}
		}

		// If the last stream in this batch is before our start time, we can stop
		if len(result.LogStreams) > 0 {
			lastStream := result.LogStreams[len(result.LogStreams)-1]
			if lastStream.LastEventTimestamp != nil && *lastStream.LastEventTimestamp < startTimeMs {
				log.Printf("Reached streams before our time window, stopping stream fetch")
				break
			}
		}

		if result.NextToken == nil {
			break
		}
		nextToken = result.NextToken
	}

	log.Printf("Found %d active streams in time window", len(streams))
	return streams, nil
}

func (lr *LogReader) getLogsForStream(ctx context.Context, groupName, streamName string, startTime time.Time, logChan chan<- *types.OutputLogEvent, wg *sync.WaitGroup) {
	defer wg.Done()

	st := startTime.UnixMilli()
	et := time.Now().UnixMilli()

	// Debug timestamp information
	log.Printf("Stream %s time window: %v to %v", streamName,
		time.UnixMilli(st).Format("2006-01-02 15:04:05"),
		time.UnixMilli(et).Format("2006-01-02 15:04:05"))

	var nextForwardToken *string
	totalEvents := 0
	seenTokens := make(map[string]bool)

	for {
		log.Printf("Getting logs for stream %s (events so far: %d) timeframe %v to %v",
			streamName,
			totalEvents,
			time.UnixMilli(st).Format("2006-01-02 15:04:05"),
			time.UnixMilli(et).Format("2006-01-02 15:04:05"))

		err := lr.rateLimiter.Wait(ctx)
		if err != nil {
			log.Printf("Rate limiter wait failed for stream %s: %v", streamName, err)
			return
		}

		input := &cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  &groupName,
			LogStreamName: &streamName,
			StartTime:     &st,
			EndTime:       &et,
			StartFromHead: aws.Bool(true), // Start from oldest logs
			Limit:         aws.Int32(10000),
			NextToken:     nextForwardToken,
		}

		result, err := lr.client.GetLogEvents(ctx, input)
		if err != nil {
			if isThrottlingError(err) {
				log.Printf("Throttling error for stream %s: %v", streamName, err)
				time.Sleep(time.Second * 2)
				continue
			}
			log.Printf("Error getting logs for stream %s: %v", streamName, err)
			return
		}

		batchSize := len(result.Events)
		if batchSize > 0 {
			firstEvent := result.Events[0]
			lastEvent := result.Events[batchSize-1]
			log.Printf("Stream %s: Got batch of %d events (time range: %v to %v)",
				streamName,
				batchSize,
				time.UnixMilli(*firstEvent.Timestamp).Format("2006-01-02 15:04:05"),
				time.UnixMilli(*lastEvent.Timestamp).Format("2006-01-02 15:04:05"))
		}

		// Process events
		for _, event := range result.Events {
			totalEvents++
			logChan <- &event
		}

		// Handle pagination
		if !tailMode {
			if result.NextForwardToken == nil {
				log.Printf("Stream %s: No next token, finishing", streamName)
				break
			}

			// Only stop if we got no events AND we've seen this token before
			currentToken := *result.NextForwardToken
			if batchSize == 0 && seenTokens[currentToken] {
				log.Printf("Stream %s: No new events and token seen before, finishing", streamName)
				break
			}

			seenTokens[currentToken] = true
		}

		nextForwardToken = result.NextForwardToken

		if tailMode {
			time.Sleep(pollInterval)
		}
	}

	log.Printf("Stream %s: Finished processing with %d total events", streamName, totalEvents)
}

func formatTimestamp(ts *int64) string {
	if ts == nil {
		return "never"
	}
	return time.UnixMilli(*ts).Format(time.RFC3339)
}

func isThrottlingError(err error) bool {
	var throttleErr *types.ThrottlingException
	return errors.As(err, &throttleErr)
}
