#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
require "net/http"
require "uri"
require_relative "runner"

module Evals
  # Raised for infrastructure errors that should be SKIP, not FAIL
  class InfraError < StandardError; end

  class AgentRunner
    MAX_TURNS = 30
    attr_reader :retries_used, :pages_fetched

    def initialize(case_path:, prompt_path:, model:, api_key: nil)
      @runner = Runner.new(case_path)
      @prompt = File.read(prompt_path)
      @model = model
      @api_key = api_key || ENV["OPENAI_API_KEY"]
      @messages = []
      @turn_count = 0

      raise "OPENAI_API_KEY required" unless @api_key
    end

    def run(task:)
      # Initialize metrics
      @retries_used = 0
      @pages_fetched = 0

      # Build system prompt with guide + task
      system_prompt = build_system_prompt(task)
      @messages = [{ role: "system", content: system_prompt }]

      # Initial user message
      @messages << { role: "user", content: "Begin the task. Use the http_request tool to interact with the Basecamp API." }

      loop do
        @turn_count += 1
        if @turn_count > MAX_TURNS
          puts "Max turns (#{MAX_TURNS}) reached"
          break
        end

        response = call_llm
        break unless response

        assistant_msg = response.dig("choices", 0, "message")
        break unless assistant_msg

        @messages << assistant_msg

        # Check for tool calls
        tool_calls = assistant_msg["tool_calls"]
        if tool_calls && !tool_calls.empty?
          # Each tool call needs its own tool message response
          tool_calls.each do |tc|
            result = process_single_tool_call(tc)
            @messages << { role: "tool", tool_call_id: tc["id"], content: JSON.generate(result) }
          end
        elsif assistant_msg["content"]&.include?("TASK_COMPLETE")
          puts "Agent signaled completion"
          break
        else
          # No tool calls and no completion signal - prompt to continue or finish
          @messages << { role: "user", content: "Continue with the task, or respond with TASK_COMPLETE if finished." }
        end
      end

      # Evaluate
      @runner.evaluate
      @runner
    end

    private

    def build_system_prompt(task)
      <<~PROMPT
        #{@prompt}

        ## Your Task

        #{task}

        ## Instructions

        - Use the http_request tool to make API calls
        - Base URL is already handled - just use paths like "/projects/1.json"
        - When you have completed the task, include "TASK_COMPLETE" in your response
        - Do NOT make up data - only use information from API responses
      PROMPT
    end

    def call_llm
      uri = URI("https://api.openai.com/v1/chat/completions")
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = true
      http.read_timeout = 120

      request = Net::HTTP::Post.new(uri)
      request["Authorization"] = "Bearer #{@api_key}"
      request["Content-Type"] = "application/json"

      body = {
        model: @model,
        messages: @messages,
        tools: [http_request_tool],
        tool_choice: "auto"
      }

      # Model-specific params
      if @model.start_with?("gpt-5", "o1", "o3")
        # Newer reasoning models: different token param, no temperature control
        body[:max_completion_tokens] = 4096
      else
        # Standard models
        body[:max_tokens] = 4096
        body[:temperature] = 0
        body[:seed] = 42
      end

      request.body = JSON.generate(body)

      response = http.request(request)

      case response.code.to_i
      when 200
        JSON.parse(response.body)
      when 401, 403
        raise InfraError, "Auth error (#{response.code}): check OPENAI_API_KEY"
      when 429
        raise InfraError, "Rate limited by OpenAI API"
      when 500..599
        raise InfraError, "OpenAI API server error (#{response.code})"
      else
        puts "LLM API error: #{response.code} - #{response.body}"
        nil
      end
    rescue Net::OpenTimeout, Net::ReadTimeout, Errno::ECONNREFUSED => e
      raise InfraError, "Connection error: #{e.message}"
    rescue OpenSSL::SSL::SSLError => e
      raise InfraError, "SSL error: #{e.message}"
    rescue InfraError
      raise  # Re-raise InfraError to propagate up
    rescue => e
      # Check for auth timeout pattern in error message
      if e.message.include?("authorization timeout") || e.message.include?("timeout")
        raise InfraError, "Timeout: #{e.message}"
      end
      puts "LLM call failed: #{e.message}"
      nil
    end

    def http_request_tool
      {
        type: "function",
        function: {
          name: "http_request",
          description: "Make an HTTP request to the Basecamp API. For POST/PUT requests, you MUST include a body object. For paginated GET requests, use paginate:true to auto-fetch all pages.",
          parameters: {
            type: "object",
            properties: {
              method: {
                type: "string",
                enum: ["GET", "POST", "PUT", "DELETE"],
                description: "HTTP method"
              },
              path: {
                type: "string",
                description: "API path (e.g., /projects/1.json)"
              },
              body: {
                type: "object",
                description: "Request body - REQUIRED for POST/PUT. For comments: {\"content\": \"your message\"}. For todos: {\"content\": \"todo text\"}."
              },
              paginate: {
                type: "boolean",
                description: "Set to true for list endpoints to auto-fetch all pages until empty. Returns aggregated items array. Only works with GET."
              }
            },
            required: ["method", "path"]
          }
        }
      }
    end

    def process_single_tool_call(tc)
      if tc.dig("function", "name") == "http_request"
        args_json = tc.dig("function", "arguments") || "{}"
        args = JSON.parse(args_json)
        if ENV["DEBUG"]
          $stderr.puts "DEBUG: Tool args: #{args_json}"
        end
        execute_http_request(args)
      else
        { error: "Unknown tool: #{tc.dig('function', 'name')}" }
      end
    end

    def execute_http_request(args)
      method = args["method"]&.upcase || "GET"
      path = args["path"]
      body = args["body"]
      paginate = args["paginate"] == true

      begin
        if paginate && method == "GET"
          execute_paginated_request(path)
        else
          response = @runner.request(method: method, url: path, body: body)
          {
            status: response[:status],
            headers: response[:headers],
            body: response[:body]
          }
        end
      rescue Runner::MaxCallsExceeded => e
        { error: e.message }
      end
    end

    MAX_PAGES = 50  # Safety cap
    MAX_RETRIES = 3  # Retry cap for 429s

    def execute_paginated_request(base_path)
      all_items = []
      page = 1

      loop do
        # Build paginated path
        separator = base_path.include?("?") ? "&" : "?"
        path = "#{base_path}#{separator}page=#{page}"

        response = fetch_with_retry(path)

        # Non-200 after retries = stop with error
        unless response[:status] == 200
          return {
            status: response[:status],
            error: "Pagination stopped at page #{page}",
            body: response[:body],
            items: all_items,
            pages_fetched: page - 1
          }
        end

        items = response[:body]

        # Empty array = pagination complete
        if items.is_a?(Array) && items.empty?
          break
        end

        # Append items
        if items.is_a?(Array)
          all_items.concat(items)
        else
          # Non-array response - return as-is
          return {
            status: 200,
            body: items,
            pages_fetched: 1
          }
        end

        page += 1

        # Safety cap
        if page > MAX_PAGES
          return {
            status: 200,
            items: all_items,
            pages_fetched: page - 1,
            warning: "Max pages (#{MAX_PAGES}) reached"
          }
        end
      end

      pages_this_request = page - 1
      @pages_fetched = (@pages_fetched || 0) + pages_this_request

      {
        status: 200,
        items: all_items,
        pages_fetched: pages_this_request,
        next_page: nil
      }
    end

    # Metrics accessor for reporting
    def metrics
      {
        retries_used: @retries_used || 0,
        pages_fetched: @pages_fetched || 0
      }
    end

    # Fetch a single page with 429 retry handling
    def fetch_with_retry(path)
      retries = 0

      loop do
        response = @runner.request(method: "GET", url: path, body: nil)

        # Success or non-429 error - return immediately
        return response unless response[:status] == 429

        # 429 rate limit - check if we should retry
        retries += 1
        @retries_used = (@retries_used || 0) + 1  # Track for metrics

        if retries > MAX_RETRIES
          return response  # Give up after max retries
        end

        # Get Retry-After header (headers are pre-normalized to lowercase)
        retry_after = response.dig(:headers, "retry-after")&.to_i || 2
        retry_after = [retry_after, 10].min  # Cap at 10 seconds

        if ENV["DEBUG"]
          $stderr.puts "DEBUG: 429 on #{path}, retry #{retries}/#{MAX_RETRIES} after #{retry_after}s"
        end

        # Skip actual sleep in stub/test mode for faster evals
        unless ENV["BCQ_STUB_MODE"] || @runner.stub_mode?
          sleep(retry_after)
        end
      end
    end

  end
end

# CLI
if __FILE__ == $0
  require "optparse"

  options = {
    model: "gpt-4o-mini",
    format: "text"
  }

  OptionParser.new do |opts|
    opts.banner = "Usage: agent_runner.rb [options] <case.yml> <prompt.md>"

    opts.on("-m", "--model MODEL", "LLM model (default: gpt-4o-mini)") do |m|
      options[:model] = m
    end

    opts.on("-t", "--task TASK", "Task description (overrides case description)") do |t|
      options[:task] = t
    end

    opts.on("-f", "--format FORMAT", "Output format: text, json") do |f|
      options[:format] = f
    end

    opts.on("-v", "--verbose", "Show agent conversation") do
      options[:verbose] = true
    end
  end.parse!

  case_path = ARGV[0]
  prompt_path = ARGV[1]

  unless case_path && prompt_path
    puts "Usage: agent_runner.rb [options] <case.yml> <prompt.md>"
    exit 1
  end

  # Resolve paths
  base_dir = File.expand_path("../..", __FILE__)
  case_path = File.join(base_dir, "cases", "#{case_path}.yml") unless File.exist?(case_path)
  prompt_path = File.join(base_dir, "..", "prompts", "#{prompt_path}.md") unless File.exist?(prompt_path)

  begin
    agent = Evals::AgentRunner.new(
      case_path: case_path,
      prompt_path: prompt_path,
      model: options[:model]
    )

    # Load case for task description
    case_data = Evals::CaseLoader.load(case_path)
    task = options[:task] || case_data[:description] || "Complete the overdue todo sweep task."

    puts "Running: #{case_data[:name]}"
    puts "Model: #{options[:model]}"
    puts "Task: #{task.lines.first.strip}..."
    puts "-" * 40

    runner = agent.run(task: task)

    puts ""
    case options[:format]
    when "json"
      output = runner.to_json.merge(
        helper_metrics: {
          retries_used: agent.retries_used,
          pages_fetched: agent.pages_fetched,
          note: "pagination/429 handled by helper"
        }
      )
      puts JSON.pretty_generate(output)
    else
      puts runner.report
      # Show helper metrics
      if agent.retries_used > 0 || agent.pages_fetched > 0
        puts ""
        puts "Helper metrics (pagination/429 handled by infrastructure):"
        puts "  retries_used: #{agent.retries_used}"
        puts "  pages_fetched: #{agent.pages_fetched}"
      end
    end

    exit(runner.passed? ? 0 : 1)
  rescue Evals::InfraError => e
    # Infra errors (auth timeout, API unreachable) = SKIP, not FAIL
    puts "[SKIP] Infrastructure error: #{e.message}"
    exit 2  # Distinct exit code for SKIP
  rescue => e
    puts "Error: #{e.message}"
    puts e.backtrace.first(5).join("\n") if ENV["DEBUG"]
    exit 1
  end
end
