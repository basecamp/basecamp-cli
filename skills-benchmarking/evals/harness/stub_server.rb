# frozen_string_literal: true

require_relative "normalizer"

module Evals
  class StubServer
    attr_reader :request_log

    def initialize(fixtures:, injections: [])
      @fixtures = fixtures || []
      @injections = injections || []
      @request_log = []
      @call_counts = Hash.new(0) # Track calls per method+path+query scope
    end

    # Process a request and return response
    # Returns { status:, headers:, body:, fixture_id:, injected: }
    def call(method:, url:, body: nil)
      method = method.to_s.upcase
      path, query = Normalizer.normalize_url(url)

      # Build scope key for call counting
      scope_key = build_scope_key(method, path, query)
      @call_counts[scope_key] += 1
      call_number = @call_counts[scope_key]

      # Check for injection first
      injection = find_injection(method, path, query, call_number)

      if injection
        response = build_response(injection[:response], injected: true)
      else
        fixture = find_fixture(method, path, query, body)
        if fixture
          response = build_response(fixture[:response], fixture_id: fixture[:id])
        else
          response = not_found_response(path)
        end
      end

      # Log the request
      log_entry = {
        method: method,
        path: path,
        query: query,
        body: body,
        status: response[:status],
        fixture_id: response[:fixture_id],
        injected: response[:injected] || false,
        call_number: call_number,
        scope_key: scope_key
      }
      @request_log << log_entry

      response
    end

    # Reset state between runs
    def reset!
      @request_log.clear
      @call_counts.clear
    end

    private

    def build_scope_key(method, path, query)
      query_str = query.empty? ? "" : "?#{query.map { |k, v| "#{k}=#{v}" }.sort.join("&")}"
      "#{method}:#{path}#{query_str}"
    end

    def find_injection(method, path, query, call_number)
      @injections.find do |inj|
        inj_method = inj[:method].to_s.upcase
        inj_path = Normalizer.normalize_path(inj[:path])
        inj_query = Normalizer.normalize_query(inj[:query])
        inj_call = inj[:on_call]

        method == inj_method &&
          path == inj_path &&
          Normalizer.queries_match?(inj_query, query) &&
          call_number == inj_call
      end
    end

    def find_fixture(method, path, query, body)
      # Phase 1: Filter to eligible fixtures (method + path must match, body if specified)
      eligible = @fixtures.each_with_index.filter_map do |fixture, index|
        fix_method = fixture[:method].to_s.upcase
        fix_path = Normalizer.normalize_path(fixture[:path])

        next unless method == fix_method && path == fix_path

        # Body constraint check (if fixture specifies body, it must match)
        if fixture.key?(:body) && !Normalizer.bodies_match?(fixture[:body], body)
          next
        end

        { fixture: fixture.merge(id: index), index: index }
      end

      return nil if eligible.empty?

      # Phase 2: Score by specificity
      scored = eligible.map do |entry|
        fixture = entry[:fixture]
        score = 0

        # Query match adds +2
        fix_query = Normalizer.normalize_query(fixture[:query])
        if !fix_query.empty? && Normalizer.queries_match?(fix_query, query)
          score += 2
        elsif !fix_query.empty?
          # Query specified but doesn't match -> ineligible
          next nil
        end

        # Body match adds +1 (already confirmed match in eligibility)
        score += 1 if fixture.key?(:body)

        { fixture: fixture, score: score, index: entry[:index] }
      end.compact

      return nil if scored.empty?

      # Phase 3: Highest score wins, tie-break by list order
      scored.max_by { |s| [s[:score], -s[:index]] }&.dig(:fixture)
    end

    def build_response(response_def, fixture_id: nil, injected: false)
      {
        status: response_def[:status] || 200,
        headers: normalize_headers(response_def[:headers] || {}),
        body: response_def[:body],
        fixture_id: fixture_id,
        injected: injected
      }
    end

    # Normalize header keys to lowercase (once, at source)
    def normalize_headers(headers)
      headers.transform_keys { |k| k.to_s.downcase }
    end

    def not_found_response(path)
      {
        status: 404,
        headers: {},
        body: { error: "Fixture not found", path: path },
        fixture_id: nil,
        injected: false
      }
    end
  end
end
