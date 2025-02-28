# go-awslogs

A fast and efficient command-line tool for fetching and tailing AWS CloudWatch logs. Built with concurrent stream processing and intelligent rate limiting to optimize log retrieval performance.

## Features

- Fetch historical logs or tail in real-time
- Concurrent processing of multiple log streams
- Built-in AWS API rate limiting
- Smart filtering of inactive streams
- Automatic pagination handling
- Configurable time ranges and polling intervals
- File-based output for historical log fetching

## Installation

```bash
go install github.com/nathanmartins/go-awslogs@latest
```

## Usage

Basic command structure:

```bash
go-awslogs [flags]
```

### Flags

- `-g, --group-name`: (Required) The CloudWatch log group name to fetch logs from
- `-t, --tail`: Enable tail mode to continuously fetch new logs
- `-i, --poll-interval`: Set the polling interval for tail mode (default: 5s)
- `-r, --time-range`: Set the time range to fetch logs from (default: 10m)
- `-h, --help`: Display help information

### Examples

Fetch the last 10 minutes of logs:
```bash
go-awslogs -g /aws/lambda/my-function
```

Tail logs in real-time:
```bash
go-awslogs -g /aws/lambda/my-function -t
```

Fetch logs from the last hour:
```bash
go-awslogs -g /aws/lambda/my-function -r 1h
```

Tail logs with custom polling interval:
```bash
go-awslogs -g /aws/lambda/my-function -t -i 10s
```

## Output

- In tail mode (`-t`), logs are printed directly to stdout
- In fetch mode (default), logs are saved to a file named after the log group (with "/" replaced by "-")
- Each log entry is written on a new line with its original timestamp

## AWS Configuration

The tool uses the AWS SDK's default configuration chain. Make sure you have:

1. AWS credentials configured in `~/.aws/credentials`, or
2. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`), or
3. IAM role when running on AWS resources

Required IAM permissions:
- `cloudwatch:DescribeLogStreams`
- `cloudwatch:GetLogEvents`

## Performance Considerations

- Uses concurrent processing with up to 3 workers for log streams
- Implements rate limiting (5 requests per second with burst of 10)
- Automatically skips inactive streams outside the requested time range
- Handles AWS API throttling with automatic retries

## License

[MIT License](LICENSE)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

Copyright (c) 2025 Nathan Martins (nathan.eua@gmail.com)