#!/usr/bin/env ruby
# frozen_string_literal: true

require_relative "case_loader"
require_relative "stub_server"
require_relative "assertions"
require_relative "normalizer"
require "json"

module Evals
  class Runner
    attr_reader :case_data, :server, :results

    def initialize(case_path)
      @case_data = CaseLoader.load(case_path)
      @server = StubServer.new(
        fixtures: @case_data[:fixtures],
        injections: @case_data[:inject]
      )
      @results = nil
      @max_calls = @case_data.dig(:assertions, :max_calls)
      @max_calls_exceeded = false
    end

    # Execute a request (called by the agent/model)
    def request(method:, url:, body: nil)
      # Check max_calls before processing (hard fail)
      if @max_calls && @server.request_log.size >= @max_calls
        @max_calls_exceeded = true
        raise MaxCallsExceeded, "max_calls limit (#{@max_calls}) exceeded"
      end

      @server.call(method: method, url: url, body: body)
    end

    # Run assertions after agent completes
    def evaluate
      checker = Assertions.new(
        assertions: @case_data[:assertions],
        request_log: @server.request_log,
        max_calls_exceeded: @max_calls_exceeded
      )

      @results = checker.check_all
      @results
    end

    def passed?
      return false unless @results
      @results.all?(&:passed)
    end

    # Returns true when running against stub server (skip real sleeps in evals)
    def stub_mode?
      true  # Runner always uses StubServer; real API would be a different class
    end

    # Format results for output
    def report
      lines = []
      status = passed? ? "PASS" : "FAIL"
      lines << "[#{@case_data[:name]}] #{status}"

      @results&.each do |result|
        prefix = result.passed ? "  ✓" : "  ✗"
        lines << "#{prefix} #{result.message}"
      end

      lines << ""
      lines << "Request log:"
      @server.request_log.each_with_index do |entry, i|
        query_str = entry[:query].empty? ? "" : "?#{entry[:query].map { |k, v| "#{k}=#{v}" }.join("&")}"
        source = if entry[:injected]
          "[INJECTED]"
        elsif entry[:fixture_id]
          "[fixture:#{entry[:fixture_id]}]"
        else
          "[404]"
        end
        lines << "  #{i + 1}. #{entry[:method]} #{entry[:path]}#{query_str} -> #{entry[:status]} #{source}"
      end

      lines.join("\n")
    end

    # JSON output for programmatic use
    def to_json
      {
        name: @case_data[:name],
        passed: passed?,
        results: @results&.map do |r|
          { passed: r.passed, message: r.message, details: r.details }
        end,
        request_log: @server.request_log,
        request_count: @server.request_log.size
      }
    end

    class MaxCallsExceeded < StandardError; end
  end

  # Simulated agent that makes requests based on a script
  class ScriptedAgent
    def initialize(runner)
      @runner = runner
    end

    # Execute a sequence of requests (for testing the harness itself)
    def execute(requests)
      requests.each do |req|
        begin
          @runner.request(
            method: req[:method],
            url: req[:url] || req[:path],
            body: req[:body]
          )
        rescue Runner::MaxCallsExceeded => e
          puts "Stopped: #{e.message}"
          break
        end
      end
    end
  end
end

# CLI entry point
if __FILE__ == $0
  require "optparse"

  options = { format: "text" }
  OptionParser.new do |opts|
    opts.banner = "Usage: runner.rb [options] <case.yml>"

    opts.on("-f", "--format FORMAT", "Output format: text, json") do |f|
      options[:format] = f
    end

    opts.on("-s", "--script FILE", "Request script (JSON array)") do |s|
      options[:script] = s
    end

    opts.on("-h", "--help", "Show help") do
      puts opts
      exit
    end
  end.parse!

  case_path = ARGV[0]
  unless case_path
    puts "Error: case file required"
    exit 1
  end

  begin
    runner = Evals::Runner.new(case_path)

    if options[:script]
      script = JSON.parse(File.read(options[:script]), symbolize_names: true)
      agent = Evals::ScriptedAgent.new(runner)
      agent.execute(script)
    end

    runner.evaluate

    case options[:format]
    when "json"
      puts JSON.pretty_generate(runner.to_json)
    else
      puts runner.report
    end

    exit(runner.passed? ? 0 : 1)
  rescue Evals::CaseLoader::ValidationError => e
    puts "Case validation error: #{e.message}"
    exit 2
  rescue => e
    puts "Error: #{e.message}"
    puts e.backtrace.first(5).join("\n") if ENV["DEBUG"]
    exit 3
  end
end
