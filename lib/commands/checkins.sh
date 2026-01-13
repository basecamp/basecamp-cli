#!/usr/bin/env bash
# checkins.sh - Automatic Check-ins (Questionnaires, Questions, and Answers)
# Covers questionnaires.md (1 endpoint), questions.md (2 endpoints), question_answers.md (2 endpoints)

cmd_checkins() {
  local action="${1:-}"

  case "$action" in
    questions) shift; _checkins_questions "$@" ;;
    question) shift; _checkins_question_show "$@" ;;
    answers) shift; _checkins_answers "$@" ;;
    answer) shift; _checkins_answer_show "$@" ;;
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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE "Use --project <id> or set default"
  fi

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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

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

  if [[ -z "$project" ]]; then
    project=$(get_project_id)
  fi

  if [[ -z "$project" ]]; then
    die "No project specified" $EXIT_USAGE
  fi

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

_help_checkins() {
  cat <<'EOF'
## bcq checkins

View automatic check-ins (questionnaires, questions, and answers).

### Usage

    bcq checkins [options]                          Show questionnaire info
    bcq checkins questions [options]                List check-in questions
    bcq checkins question <id> [options]            Show a specific question
    bcq checkins answers <question_id> [options]    List answers to a question
    bcq checkins answer <id> [options]              Show a specific answer

### Options

    --in, -p <project>        Project ID
    --questionnaire, -q <id>  Questionnaire ID (auto-detected from dock)

### Examples

    # View questionnaire info
    bcq checkins --project 123

    # List all check-in questions
    bcq checkins questions --project 123

    # View a specific question
    bcq checkins question 456 --project 123

    # View answers for a question
    bcq checkins answers 456 --project 123

    # View a specific answer
    bcq checkins answer 789 --project 123

### Notes

Check-ins are "Automatic Check-ins" in Basecamp - recurring questions that
collect answers from team members on a schedule (e.g., "What did you work
on today?").

Each project has one questionnaire containing multiple questions. Each
question has a schedule (frequency, days, time) and collects answers from
participants.

EOF
}
