#!/usr/bin/env bash
# checkins.sh - Automatic Check-ins (Questionnaires, Questions, and Answers)
# Covers questionnaires.md (1 endpoint), questions.md (2 endpoints), question_answers.md (2 endpoints)

cmd_checkins() {
  local action="${1:-}"

  case "$action" in
    questions) shift; _checkins_questions "$@" ;;
    question) shift; _checkins_question "$@" ;;
    answers) shift; _checkins_answers "$@" ;;
    answer) shift; _checkins_answer "$@" ;;
    --help|-h) _help_checkins ;;
    "")
      _checkins_show "$@"
      ;;
    -*)
      # Flags go to questionnaire show
      _checkins_show "$@"
      ;;
    *)
      # If it looks like a numeric ID, could be question or answer - show question
      if [[ "$action" =~ ^[0-9]+$ ]]; then
        _checkins_question_show "$@"
      else
        die "Unknown checkins action: $action" $EXIT_USAGE "Run: bcq checkins --help"
      fi
      ;;
  esac
}

# Router for question subcommand
_checkins_question() {
  local action="${1:-}"
  case "$action" in
    create) shift; _checkins_question_create "$@" ;;
    update) shift; _checkins_question_update "$@" ;;
    *)
      # Default to show
      _checkins_question_show "$@"
      ;;
  esac
}

# Router for answer subcommand
_checkins_answer() {
  local action="${1:-}"
  case "$action" in
    create) shift; _checkins_answer_create "$@" ;;
    update) shift; _checkins_answer_update "$@" ;;
    *)
      # Default to show
      _checkins_answer_show "$@"
      ;;
  esac
}

# Get questionnaire ID from project dock
_get_questionnaire_id() {
  local project="$1"
  local dock
  dock=$(api_get "/projects/$project.json" | jq -r '.dock[] | select(.name == "questionnaire") | .id')
  if [[ -z "$dock" ]] || [[ "$dock" == "null" ]]; then
    die "No questionnaire found for project $project" $EXIT_NOT_FOUND
  fi
  echo "$dock"
}

# GET /buckets/:bucket/questionnaires/:questionnaire.json
_checkins_show() {
  local project="" questionnaire_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --questionnaire|-q)
        [[ -z "${2:-}" ]] && die "--questionnaire requires a value" $EXIT_USAGE
        questionnaire_id="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$questionnaire_id" ]]; then
    questionnaire_id=$(_get_questionnaire_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/questionnaires/$questionnaire_id.json")

  local questions_count name
  questions_count=$(echo "$response" | jq -r '.questions_count // 0')
  name=$(echo "$response" | jq -r '.name // "Automatic Check-ins"')
  local summary="$name ($questions_count questions)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "questions" "bcq checkins questions --project $project" "View questions")"
  )

  output "$response" "$summary" "$bcs" "_checkins_show_md"
}

_checkins_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local name bucket
  name=$(echo "$data" | jq -r '.name // "Automatic Check-ins"')
  bucket=$(echo "$data" | jq -r '.bucket.name // "-"')

  echo "## $name in $bucket"
  echo
  echo "**Questions**: $(echo "$data" | jq -r '.questions_count // 0')"
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/questionnaires/:questionnaire/questions.json
_checkins_questions() {
  local project="" questionnaire_id=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --questionnaire|-q)
        [[ -z "${2:-}" ]] && die "--questionnaire requires a value" $EXIT_USAGE
        questionnaire_id="$2"
        shift 2
        ;;
      *) shift ;;
    esac
  done

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  if [[ -z "$questionnaire_id" ]]; then
    questionnaire_id=$(_get_questionnaire_id "$project")
  fi

  local response
  response=$(api_get "/buckets/$project/questionnaires/$questionnaire_id/questions.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count check-in questions"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "question" "bcq checkins question <id> --project $project" "View question details")" \
    "$(breadcrumb "answers" "bcq checkins answers <question_id> --project $project" "View answers")"
  )

  output "$response" "$summary" "$bcs" "_checkins_questions_md"
}

_checkins_questions_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Check-in Questions ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No questions configured*"
  else
    echo "| # | Question | Schedule | Answers | Paused |"
    echo "|---|----------|----------|---------|--------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.title | .[0:40]) | \(.schedule.frequency // "-") | \(.answers_count // 0) | \(.paused // false) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/questions/:id.json
_checkins_question_show() {
  local question_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$question_id" ]]; then
          question_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$question_id" ]]; then
    die "Question ID required" $EXIT_USAGE "Usage: bcq checkins question <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/questions/$question_id.json")

  local title answers_count
  title=$(echo "$response" | jq -r '.title // "Question"')
  answers_count=$(echo "$response" | jq -r '.answers_count // 0')
  local summary="$title ($answers_count answers)"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "answers" "bcq checkins answers $question_id --project $project" "View answers")" \
    "$(breadcrumb "questions" "bcq checkins questions --project $project" "View all questions")"
  )

  output "$response" "$summary" "$bcs" "_checkins_question_show_md"
}

_checkins_question_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local title paused schedule_freq schedule_days
  title=$(echo "$data" | jq -r '.title // "Question"')
  paused=$(echo "$data" | jq -r '.paused // false')
  schedule_freq=$(echo "$data" | jq -r '.schedule.frequency // "not set"')
  schedule_days=$(echo "$data" | jq -r '[.schedule.days[]? | if . == 0 then "Sun" elif . == 1 then "Mon" elif . == 2 then "Tue" elif . == 3 then "Wed" elif . == 4 then "Thu" elif . == 5 then "Fri" else "Sat" end] | join(", ") // ""')
  local schedule_time
  schedule_time=$(echo "$data" | jq -r '"at \(.schedule.hour // 0):\(if .schedule.minute < 10 then "0" else "" end)\(.schedule.minute // 0)"')

  echo "## $title"
  echo
  echo "**Schedule**: $schedule_freq on $schedule_days $schedule_time"
  echo "**Answers**: $(echo "$data" | jq -r '.answers_count // 0')"
  echo "**Paused**: $paused"
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/questionnaires/:questionnaire/questions.json
_checkins_question_create() {
  local project="" questionnaire_id="" title="" frequency="" time_of_day="" days=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --questionnaire|-q)
        [[ -z "${2:-}" ]] && die "--questionnaire requires a value" $EXIT_USAGE
        questionnaire_id="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --frequency|-f)
        [[ -z "${2:-}" ]] && die "--frequency requires a value" $EXIT_USAGE
        frequency="$2"
        shift 2
        ;;
      --time)
        [[ -z "${2:-}" ]] && die "--time requires a value" $EXIT_USAGE
        time_of_day="$2"
        shift 2
        ;;
      --days|-d)
        [[ -z "${2:-}" ]] && die "--days requires a value" $EXIT_USAGE
        days="$2"
        shift 2
        ;;
      --help|-h)
        _help_checkins_question_create
        return 0
        ;;
      *) shift ;;
    esac
  done

  if [[ -z "$title" ]]; then
    _help_checkins_question_create
    die "Missing required --title flag" $EXIT_USAGE
  fi

  # Resolve project
  project=$(require_project_id "${project:-}")

  if [[ -z "$questionnaire_id" ]]; then
    questionnaire_id=$(_get_questionnaire_id "$project")
  fi

  # Default schedule values
  frequency="${frequency:-every_day}"
  time_of_day="${time_of_day:-5:00pm}"
  days="${days:-1,2,3,4,5}"

  # Convert days to JSON array (e.g., "1,2,3,4,5" -> ["1","2","3","4","5"])
  local days_json
  days_json=$(echo "$days" | tr ',' '\n' | jq -R . | jq -s .)

  local payload
  payload=$(jq -n \
    --arg title "$title" \
    --arg frequency "$frequency" \
    --arg time_of_day "$time_of_day" \
    --argjson days "$days_json" \
    '{question: {title: $title, schedule: {frequency: $frequency, time_of_day: $time_of_day, days: $days}}}')

  local response
  response=$(api_post "/buckets/$project/questionnaires/$questionnaire_id/questions.json" "$payload")

  local question_title question_id
  question_title=$(echo "$response" | jq -r '.title // "Question"')
  question_id=$(echo "$response" | jq -r '.id')
  local summary="Created: $question_title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "question" "bcq checkins question $question_id --project $project" "View question")" \
    "$(breadcrumb "questions" "bcq checkins questions --project $project" "View all questions")"
  )

  output "$response" "$summary" "$bcs"
}

_help_checkins_question_create() {
  cat << 'EOF'
bcq checkins question create - Create a new check-in question

USAGE
  bcq checkins question create --title "question" [options]

OPTIONS
  --title, -t <text>        Question text (required)
  --in, --project, -p <id>  Project ID or name
  --questionnaire, -q <id>  Questionnaire ID (auto-detected)
  --frequency, -f <freq>    Schedule frequency (default: every_day)
                            Options: every_day, every_week, every_other_week,
                                     every_month, on_certain_days
  --time <time>             Time to ask (default: 5:00pm)
  --days, -d <days>         Days to ask, comma-separated (default: 1,2,3,4,5)
                            0=Sun, 1=Mon, 2=Tue, 3=Wed, 4=Thu, 5=Fri, 6=Sat

EXAMPLES
  bcq checkins question create --title "What did you work on today?" --in 123
  bcq checkins question create --title "Weekly status?" --frequency every_week --days 1 --in 123
  bcq checkins question create --title "Daily standup" --time "9:00am" --in "My Project"
EOF
}

# PUT /buckets/:bucket/questions/:id.json
_checkins_question_update() {
  local project="" question_id="" title="" frequency="" time_of_day="" days=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --title|-t)
        [[ -z "${2:-}" ]] && die "--title requires a value" $EXIT_USAGE
        title="$2"
        shift 2
        ;;
      --frequency|-f)
        [[ -z "${2:-}" ]] && die "--frequency requires a value" $EXIT_USAGE
        frequency="$2"
        shift 2
        ;;
      --time)
        [[ -z "${2:-}" ]] && die "--time requires a value" $EXIT_USAGE
        time_of_day="$2"
        shift 2
        ;;
      --days|-d)
        [[ -z "${2:-}" ]] && die "--days requires a value" $EXIT_USAGE
        days="$2"
        shift 2
        ;;
      --help|-h)
        _help_checkins_question_update
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$question_id" ]]; then
          question_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$question_id" ]]; then
    _help_checkins_question_update
    die "Question ID required" $EXIT_USAGE
  fi

  # Resolve project
  project=$(require_project_id "${project:-}")

  # Build payload with only provided fields
  local payload='{}'
  payload=$(echo "$payload" | jq '{question: {}}')

  if [[ -n "$title" ]]; then
    payload=$(echo "$payload" | jq --arg t "$title" '.question.title = $t')
  fi

  if [[ -n "$frequency" ]] || [[ -n "$time_of_day" ]] || [[ -n "$days" ]]; then
    payload=$(echo "$payload" | jq '.question.schedule = {}')
    if [[ -n "$frequency" ]]; then
      payload=$(echo "$payload" | jq --arg f "$frequency" '.question.schedule.frequency = $f')
    fi
    if [[ -n "$time_of_day" ]]; then
      payload=$(echo "$payload" | jq --arg t "$time_of_day" '.question.schedule.time_of_day = $t')
    fi
    if [[ -n "$days" ]]; then
      local days_json
      days_json=$(echo "$days" | tr ',' '\n' | jq -R . | jq -s .)
      payload=$(echo "$payload" | jq --argjson d "$days_json" '.question.schedule.days = $d')
    fi
  fi

  local response
  response=$(api_put "/buckets/$project/questions/$question_id.json" "$payload")

  local question_title
  question_title=$(echo "$response" | jq -r '.title // "Question"')
  local summary="Updated: $question_title"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "question" "bcq checkins question $question_id --project $project" "View question")" \
    "$(breadcrumb "questions" "bcq checkins questions --project $project" "View all questions")"
  )

  output "$response" "$summary" "$bcs"
}

_help_checkins_question_update() {
  cat << 'EOF'
bcq checkins question update - Update a check-in question

USAGE
  bcq checkins question update <id> [options]

OPTIONS
  <id>                      Question ID (required)
  --in, --project, -p <id>  Project ID or name
  --title, -t <text>        New question text
  --frequency, -f <freq>    New schedule frequency
  --time <time>             New time to ask
  --days, -d <days>         New days to ask

EXAMPLES
  bcq checkins question update 456 --title "New question?" --in 123
  bcq checkins question update 456 --frequency every_week --days 1 --in 123
EOF
}

# GET /buckets/:bucket/questions/:question/answers.json
_checkins_answers() {
  local question_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$question_id" ]]; then
          question_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$question_id" ]]; then
    die "Question ID required" $EXIT_USAGE "Usage: bcq checkins answers <question_id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/questions/$question_id/answers.json")

  local count
  count=$(echo "$response" | jq 'length')
  local summary="$count answers"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "answer" "bcq checkins answer <id> --project $project" "View answer details")" \
    "$(breadcrumb "question" "bcq checkins question $question_id --project $project" "View question")"
  )

  output "$response" "$summary" "$bcs" "_checkins_answers_md"
}

_checkins_answers_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  echo "## Check-in Answers ($summary)"
  echo

  local count
  count=$(echo "$data" | jq 'length')

  if [[ "$count" -eq 0 ]]; then
    echo "*No answers yet*"
  else
    echo "| # | Date | Author | Preview |"
    echo "|---|------|--------|---------|"
    echo "$data" | jq -r '.[] | "| \(.id) | \(.group_on // .created_at | .[0:10]) | \(.creator.name // "-") | \(.content | gsub("<[^>]*>"; "") | gsub("\\n"; " ") | .[0:40]) |"'
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# GET /buckets/:bucket/question_answers/:id.json
_checkins_answer_show() {
  local answer_id="" project=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$answer_id" ]]; then
          answer_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$answer_id" ]]; then
    die "Answer ID required" $EXIT_USAGE "Usage: bcq checkins answer <id> --project <project>"
  fi

  # Resolve project (supports names, IDs, and config fallback)
  project=$(require_project_id "${project:-}")

  local response
  response=$(api_get "/buckets/$project/question_answers/$answer_id.json")

  local author group_on
  author=$(echo "$response" | jq -r '.creator.name // "Unknown"')
  group_on=$(echo "$response" | jq -r '.group_on // .created_at | .[0:10]')
  local summary="Answer by $author on $group_on"

  local question_id
  question_id=$(echo "$response" | jq -r '.parent.id // ""')

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "question" "bcq checkins question $question_id --project $project" "View question")" \
    "$(breadcrumb "answers" "bcq checkins answers $question_id --project $project" "View all answers")"
  )

  output "$response" "$summary" "$bcs" "_checkins_answer_show_md"
}

_checkins_answer_show_md() {
  local data="$1"
  local summary="$2"
  local breadcrumbs="$3"

  local author group_on content question
  author=$(echo "$data" | jq -r '.creator.name // "Unknown"')
  group_on=$(echo "$data" | jq -r '.group_on // .created_at | .[0:10]')
  content=$(echo "$data" | jq -r '.content // ""' | sed 's/<[^>]*>//g')
  question=$(echo "$data" | jq -r '.parent.title // "Question"')

  echo "## Answer by $author"
  echo
  echo "**Question**: $question"
  echo "**Date**: $group_on"
  echo
  if [[ -n "$content" ]] && [[ "$content" != "null" ]]; then
    echo "$content"
  fi
  echo
  md_breadcrumbs "$breadcrumbs"
}

# POST /buckets/:bucket/questions/:question/answers.json
_checkins_answer_create() {
  local project="" question_id="" content="" group_on=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --question|-q)
        [[ -z "${2:-}" ]] && die "--question requires a value" $EXIT_USAGE
        question_id="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      --date|--group-on)
        [[ -z "${2:-}" ]] && die "--date requires a value" $EXIT_USAGE
        group_on="$2"
        shift 2
        ;;
      --help|-h)
        _help_checkins_answer_create
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$question_id" ]]; then
          question_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$content" ]]; then
    _help_checkins_answer_create
    die "Missing required --content flag" $EXIT_USAGE
  fi

  if [[ -z "$question_id" ]]; then
    _help_checkins_answer_create
    die "Question ID required" $EXIT_USAGE
  fi

  # Resolve project
  project=$(require_project_id "${project:-}")

  # Build payload
  local payload
  payload=$(jq -n --arg content "<div>$content</div>" '{question_answer: {content: $content}}')

  if [[ -n "$group_on" ]]; then
    payload=$(echo "$payload" | jq --arg g "$group_on" '.question_answer.group_on = $g')
  fi

  local response
  response=$(api_post "/buckets/$project/questions/$question_id/answers.json" "$payload")

  local answer_id author
  answer_id=$(echo "$response" | jq -r '.id')
  author=$(echo "$response" | jq -r '.creator.name // "You"')
  local summary="Answer created by $author"

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "answer" "bcq checkins answer $answer_id --project $project" "View answer")" \
    "$(breadcrumb "answers" "bcq checkins answers $question_id --project $project" "View all answers")"
  )

  output "$response" "$summary" "$bcs"
}

_help_checkins_answer_create() {
  cat << 'EOF'
bcq checkins answer create - Create a check-in answer

USAGE
  bcq checkins answer create <question_id> --content "answer" [options]

OPTIONS
  <question_id>             Question ID to answer (required)
  --content, --body, -b     Answer content (required)
  --in, --project, -p <id>  Project ID or name
  --date, --group-on <date> Date to group answer (ISO 8601, e.g., 2024-01-22)

EXAMPLES
  bcq checkins answer create 456 --content "Worked on API docs" --in 123
  bcq checkins answer create 456 --content "Status update" --date 2024-01-22 --in "My Project"
EOF
}

# PUT /buckets/:bucket/question_answers/:id.json
_checkins_answer_update() {
  local project="" answer_id="" content=""

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --in|--project|-p)
        [[ -z "${2:-}" ]] && die "--project requires a value" $EXIT_USAGE
        project="$2"
        shift 2
        ;;
      --content|--body|-b)
        [[ -z "${2:-}" ]] && die "--content requires a value" $EXIT_USAGE
        content="$2"
        shift 2
        ;;
      --help|-h)
        _help_checkins_answer_update
        return 0
        ;;
      *)
        if [[ "$1" =~ ^[0-9]+$ ]] && [[ -z "$answer_id" ]]; then
          answer_id="$1"
        fi
        shift
        ;;
    esac
  done

  if [[ -z "$answer_id" ]]; then
    _help_checkins_answer_update
    die "Answer ID required" $EXIT_USAGE
  fi

  if [[ -z "$content" ]]; then
    _help_checkins_answer_update
    die "Missing required --content flag" $EXIT_USAGE
  fi

  # Resolve project
  project=$(require_project_id "${project:-}")

  local payload
  payload=$(jq -n --arg content "<div>$content</div>" '{question_answer: {content: $content}}')

  # PUT returns 204 No Content on success, so we need to fetch the answer after
  api_put "/buckets/$project/question_answers/$answer_id.json" "$payload" >/dev/null

  # Fetch the updated answer
  local response
  response=$(api_get "/buckets/$project/question_answers/$answer_id.json")

  local author
  author=$(echo "$response" | jq -r '.creator.name // "You"')
  local summary="Answer updated"

  local question_id
  question_id=$(echo "$response" | jq -r '.parent.id // ""')

  local bcs
  bcs=$(breadcrumbs \
    "$(breadcrumb "answer" "bcq checkins answer $answer_id --project $project" "View answer")" \
    "$(breadcrumb "answers" "bcq checkins answers $question_id --project $project" "View all answers")"
  )

  output "$response" "$summary" "$bcs"
}

_help_checkins_answer_update() {
  cat << 'EOF'
bcq checkins answer update - Update a check-in answer

USAGE
  bcq checkins answer update <id> --content "new content" [options]

OPTIONS
  <id>                      Answer ID (required)
  --content, --body, -b     New answer content (required)
  --in, --project, -p <id>  Project ID or name

EXAMPLES
  bcq checkins answer update 789 --content "Updated answer" --in 123
EOF
}

_help_checkins() {
  cat <<'EOF'
## bcq checkins

Manage automatic check-ins (questionnaires, questions, and answers).

### Usage

    bcq checkins [options]                                Show questionnaire info
    bcq checkins questions [options]                      List check-in questions
    bcq checkins question <id> [options]                  Show a specific question
    bcq checkins question create --title "?" [options]    Create a new question
    bcq checkins question update <id> [options]           Update a question
    bcq checkins answers <question_id> [options]          List answers to a question
    bcq checkins answer <id> [options]                    Show a specific answer
    bcq checkins answer create <q_id> --content [opts]    Create an answer
    bcq checkins answer update <id> --content [opts]      Update an answer

### Options

    --in, -p <project>        Project ID or name
    --questionnaire, -q <id>  Questionnaire ID (auto-detected from dock)

### Examples

    # View questionnaire info
    bcq checkins --project 123

    # List all check-in questions
    bcq checkins questions --project 123

    # Create a new daily check-in question
    bcq checkins question create --title "What did you work on today?" --in 123

    # Create a weekly check-in
    bcq checkins question create --title "Weekly status?" --frequency every_week --days 1 --in 123

    # Update a question's schedule
    bcq checkins question update 456 --time "9:00am" --in 123

    # View answers for a question
    bcq checkins answers 456 --project 123

    # Create an answer to a question
    bcq checkins answer create 456 --content "Worked on API docs" --in 123

    # Update an answer
    bcq checkins answer update 789 --content "Updated content" --in 123

### Notes

Check-ins are "Automatic Check-ins" in Basecamp - recurring questions that
collect answers from team members on a schedule (e.g., "What did you work
on today?").

Each project has one questionnaire containing multiple questions. Each
question has a schedule (frequency, days, time) and collects answers from
participants.

EOF
}
