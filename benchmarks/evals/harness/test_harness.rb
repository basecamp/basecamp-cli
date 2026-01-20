#!/usr/bin/env ruby
# frozen_string_literal: true

# Quick test suite for the eval harness

require_relative "normalizer"
require_relative "stub_server"
require_relative "assertions"
require_relative "case_loader"
require_relative "runner"

module Evals
  module Tests
    class << self
      def run_all
        @passed = 0
        @failed = 0

        puts "=== Normalizer Tests ==="
        test_path_normalization
        test_query_normalization
        test_url_parsing
        test_body_serialization

        puts "\n=== StubServer Tests ==="
        test_fixture_matching_basic
        test_fixture_specificity
        test_injection_precedence
        test_call_counting

        puts "\n=== Assertions Tests ==="
        test_required_sequence
        test_required_sequence_occurrence
        test_strict_sequence
        test_forbidden
        test_end_state
        test_max_calls
        test_max_calls_hard_fail

        puts "\n=== Integration Tests ==="
        test_pagination_case
        test_retry_429_case

        puts "\n" + "=" * 40
        puts "Passed: #{@passed}, Failed: #{@failed}"
        @failed == 0
      end

      private

      def assert(condition, message)
        if condition
          print "."
          @passed += 1
        else
          puts "\n  FAIL: #{message}"
          @failed += 1
        end
      end

      def test_path_normalization
        assert Normalizer.normalize_path("/foo/bar/") == "foo/bar", "strips slashes"
        assert Normalizer.normalize_path("/foo/bar") == "foo/bar", "strips leading slash"
        assert Normalizer.normalize_path("foo/bar/") == "foo/bar", "strips trailing slash"
        assert Normalizer.normalize_path("/Projects.json") == "Projects.json", "preserves case"
      end

      def test_query_normalization
        assert Normalizer.normalize_query({page: 2}) == {"page" => "2"}, "coerces int to string"
        assert Normalizer.normalize_query({"page" => "2"}) == {"page" => "2"}, "keeps string"
        assert Normalizer.normalize_query({b: "2", a: "1"}) == {"a" => "1", "b" => "2"}, "sorts keys"
        assert Normalizer.normalize_query({"type[]" => ["B", "A"]}) == {"type" => ["A", "B"]}, "strips brackets, sorts array"
      end

      def test_url_parsing
        path, query = Normalizer.normalize_url("https://api.example.com/foo.json?page=2")
        assert path == "foo.json", "extracts path from full URL"
        assert query == {"page" => "2"}, "extracts query from full URL"

        path, query = Normalizer.normalize_url("/foo.json?page=1&page=2")
        assert path == "foo.json", "extracts path from relative URL"
        assert query == {"page" => ["1", "2"]}, "handles repeated keys"
      end

      def test_body_serialization
        assert Normalizer.serialize_body({b: 1, a: 2}) == '{"a":2,"b":1}', "sorts keys in JSON"
        assert Normalizer.body_contains?({content: "BenchChain abc"}, "BenchChain"), "body_contains works"
        assert !Normalizer.body_contains?({content: "other"}, "BenchChain"), "body_contains rejects"
      end

      def test_fixture_matching_basic
        server = StubServer.new(fixtures: [
          {method: "GET", path: "/projects.json", response: {status: 200, body: []}}
        ])

        resp = server.call(method: "GET", url: "/projects.json")
        assert resp[:status] == 200, "matches basic fixture"
        assert resp[:fixture_id] == 0, "returns fixture_id"

        resp = server.call(method: "GET", url: "/unknown.json")
        assert resp[:status] == 404, "returns 404 for unknown"
      end

      def test_fixture_specificity
        server = StubServer.new(fixtures: [
          {method: "GET", path: "/todos.json", response: {status: 200, body: []}},  # catch-all
          {method: "GET", path: "/todos.json", query: {page: "1"}, response: {status: 200, body: [{id: 1}]}}
        ])

        resp = server.call(method: "GET", url: "/todos.json?page=1")
        assert resp[:body] == [{id: 1}], "specific fixture wins over catch-all"

        resp = server.call(method: "GET", url: "/todos.json?page=99")
        assert resp[:body] == [], "catch-all handles unknown pages"
      end

      def test_injection_precedence
        server = StubServer.new(
          fixtures: [
            {method: "GET", path: "/todos.json", query: {page: "2"}, response: {status: 200, body: [{id: 2}]}}
          ],
          injections: [
            {method: "GET", path: "/todos.json", query: {page: "2"}, on_call: 1, response: {status: 429, body: {error: "rate limited"}}}
          ]
        )

        resp1 = server.call(method: "GET", url: "/todos.json?page=2")
        assert resp1[:status] == 429, "first call gets injection"
        assert resp1[:injected] == true, "marked as injected"

        resp2 = server.call(method: "GET", url: "/todos.json?page=2")
        assert resp2[:status] == 200, "second call gets fixture"
        assert resp2[:injected] == false, "not marked as injected"
      end

      def test_call_counting
        server = StubServer.new(fixtures: [
          {method: "GET", path: "/todos.json", response: {status: 200, body: []}}
        ])

        server.call(method: "GET", url: "/todos.json?page=1")
        server.call(method: "GET", url: "/todos.json?page=1")
        server.call(method: "GET", url: "/todos.json?page=2")

        log = server.request_log
        assert log[0][:call_number] == 1, "first call to page=1 is #1"
        assert log[1][:call_number] == 2, "second call to page=1 is #2"
        assert log[2][:call_number] == 1, "first call to page=2 is #1"
      end

      def test_required_sequence
        log = [
          {method: "GET", path: "projects.json", query: {}, status: 200},
          {method: "GET", path: "todos.json", query: {"page" => "1"}, status: 200},
          {method: "GET", path: "todos.json", query: {"page" => "2"}, status: 200}
        ]

        checker = Assertions.new(
          assertions: {
            required_sequence: [
              {method: "GET", path: "/projects.json"},
              {method: "GET", path: "/todos.json", query: {page: "1"}},
              {method: "GET", path: "/todos.json", query: {page: "2"}}
            ]
          },
          request_log: log
        )

        assert checker.passed?, "sequence matches"
      end

      def test_required_sequence_occurrence
        log = [
          {method: "GET", path: "todos.json", query: {"page" => "2"}, status: 429},
          {method: "GET", path: "todos.json", query: {"page" => "2"}, status: 200}
        ]

        checker = Assertions.new(
          assertions: {
            required_sequence: [
              {method: "GET", path: "/todos.json", query: {page: "2"}, occurrence: 1, expect_status: 429},
              {method: "GET", path: "/todos.json", query: {page: "2"}, occurrence: 2, expect_status: 200}
            ]
          },
          request_log: log
        )

        assert checker.passed?, "occurrence + expect_status works"

        # Test failure case
        checker_fail = Assertions.new(
          assertions: {
            required_sequence: [
              {method: "GET", path: "/todos.json", query: {page: "2"}, occurrence: 1, expect_status: 200}
            ]
          },
          request_log: log
        )

        assert !checker_fail.passed?, "expect_status mismatch fails"
      end

      def test_forbidden
        log = [
          {method: "POST", path: "comments.json", query: {}, status: 201, body: {content: "BenchChain test"}}
        ]

        checker = Assertions.new(
          assertions: {
            forbidden: [
              {method: "POST", path: "/comments.json", body_contains: "BenchChain"}
            ]
          },
          request_log: log
        )

        assert !checker.passed?, "forbidden with body_contains fails"
      end

      def test_end_state
        log = [
          {method: "POST", path: "completion.json", query: {}, status: 200}
        ]

        checker = Assertions.new(
          assertions: {
            end_state: [
              {method: "POST", path: "/completion.json", count: 1}
            ]
          },
          request_log: log
        )

        assert checker.passed?, "end_state count=1 passes"

        checker_fail = Assertions.new(
          assertions: {
            end_state: [
              {method: "POST", path: "/completion.json", count: 2}
            ]
          },
          request_log: log
        )

        assert !checker_fail.passed?, "end_state count=2 fails when actual=1"
      end

      def test_max_calls
        log = (1..25).map { {method: "GET", path: "x", query: {}, status: 200} }

        checker = Assertions.new(
          assertions: {max_calls: 20},
          request_log: log
        )

        results = checker.check_all
        assert !results.first.passed, "max_calls exceeded fails"
      end

      def test_max_calls_hard_fail
        # When runner catches the limit, log has exactly limit entries but exceeded flag is set
        log = (1..15).map { {method: "GET", path: "x", query: {}, status: 200} }

        checker = Assertions.new(
          assertions: {max_calls: 15},
          request_log: log,
          max_calls_exceeded: true
        )

        results = checker.check_all
        assert !results.first.passed, "max_calls hard fail when exceeded flag set"
        assert results.first.message.include?("attempted call 16"), "message mentions attempted call"
      end

      def test_strict_sequence
        log = [
          {method: "GET", path: "a.json", query: {}, status: 200},
          {method: "GET", path: "extra.json", query: {}, status: 200},  # unexpected
          {method: "GET", path: "b.json", query: {}, status: 200}
        ]

        # Non-strict should pass (extra calls allowed between)
        checker = Assertions.new(
          assertions: {
            required_sequence: [
              {method: "GET", path: "/a.json"},
              {method: "GET", path: "/b.json"}
            ],
            strict: false
          },
          request_log: log
        )
        assert checker.passed?, "non-strict allows extra calls"

        # Strict should fail
        checker_strict = Assertions.new(
          assertions: {
            required_sequence: [
              {method: "GET", path: "/a.json"},
              {method: "GET", path: "/b.json"}
            ],
            strict: true
          },
          request_log: log
        )
        assert !checker_strict.passed?, "strict fails with extra calls"
      end

      def test_pagination_case
        case_path = File.expand_path("../../cases/pagination.yml", __FILE__)
        return unless File.exist?(case_path)

        runner = Runner.new(case_path)

        # Simulate correct agent behavior
        requests = [
          {method: "GET", url: "/projects/1.json"},
          {method: "GET", url: "/buckets/1/todosets/10/todolists.json"},
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=1"},
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=2"},
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=3"},
          {method: "GET", url: "/buckets/1/recordings/1003/comments.json"},
          {method: "POST", url: "/buckets/1/recordings/1003/comments.json", body: {content: "BenchChain test"}},
          {method: "POST", url: "/buckets/1/todos/1003/completion.json"}
        ]

        agent = ScriptedAgent.new(runner)
        agent.execute(requests)
        runner.evaluate

        assert runner.passed?, "pagination case passes with correct behavior"
      end

      def test_retry_429_case
        case_path = File.expand_path("../../cases/retry_429.yml", __FILE__)
        return unless File.exist?(case_path)

        runner = Runner.new(case_path)

        # Simulate correct agent behavior with retry
        requests = [
          {method: "GET", url: "/projects/1.json"},
          {method: "GET", url: "/buckets/1/todosets/10/todolists.json"},
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=1"},
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=2"},  # gets 429
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=2"},  # retry, gets 200
          {method: "GET", url: "/buckets/1/todolists/100/todos.json?page=3"},
          {method: "GET", url: "/buckets/1/recordings/1003/comments.json"},
          {method: "POST", url: "/buckets/1/recordings/1003/comments.json", body: {content: "BenchChain test"}},
          {method: "POST", url: "/buckets/1/todos/1003/completion.json"}
        ]

        agent = ScriptedAgent.new(runner)
        agent.execute(requests)
        runner.evaluate

        # Verify injection worked
        log = runner.server.request_log
        page2_calls = log.select { |e| e[:path] == "buckets/1/todolists/100/todos.json" && e[:query]["page"] == "2" }
        assert page2_calls[0][:status] == 429, "first page 2 call got 429"
        assert page2_calls[0][:injected] == true, "first page 2 call was injected"
        assert page2_calls[1][:status] == 200, "second page 2 call got 200"

        assert runner.passed?, "retry_429 case passes with correct retry behavior"
      end
    end
  end
end

if __FILE__ == $0
  success = Evals::Tests.run_all
  exit(success ? 0 : 1)
end
