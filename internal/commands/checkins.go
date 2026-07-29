package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

var checkinsNow = time.Now

// NewCheckinsCmd creates the checkins command group.
func NewCheckinsCmd() *cobra.Command {
	var project string
	var questionnaireID string

	cmd := &cobra.Command{
		Use:     "checkins",
		Aliases: []string{"checkin"},
		Short:   "Manage automatic check-ins",
		Long: `Manage automatic check-ins (questionnaires, questions, and answers).

Check-ins are recurring questions that collect answers from team members
on a schedule (e.g., "What did you work on today?").`,
		Annotations: map[string]string{"agent_notes": "Each project has one questionnaire (check-in container)\nQuestions are asked on a recurring schedule\nAnswers are posted by team members in response"},
	}

	cmd.PersistentFlags().StringVarP(&project, "project", "p", "", "Project ID or name")
	cmd.PersistentFlags().StringVar(&project, "in", "", "Project ID (alias for --project)")
	cmd.PersistentFlags().StringVar(&questionnaireID, "questionnaire", "", "Questionnaire ID (auto-detected)")

	cmd.AddCommand(
		newCheckinsQuestionsCmd(&project, &questionnaireID),
		newCheckinsQuestionCmd(&project),
		newCheckinsAnswersCmd(&project, &questionnaireID),
		newCheckinsAnswerCmd(&project),
	)

	return cmd
}

func newCheckinsQuestionsCmd(project, questionnaireID *string) *cobra.Command {
	var limit int
	var page int
	var all bool

	cmd := &cobra.Command{
		Use:   "questions",
		Short: "List check-in questions",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			// Validate flag combinations
			if all && limit > 0 {
				return output.ErrUsage("--all and --limit are mutually exclusive")
			}
			if page > 0 && (all || limit > 0) {
				return output.ErrUsage("--page cannot be combined with --all or --limit")
			}
			if page > 1 {
				return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
			}

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get questionnaire ID
			resolvedQuestionnaireID := *questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			qID, err := strconv.ParseInt(resolvedQuestionnaireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid questionnaire ID")
			}

			// Build pagination options
			opts := &basecamp.QuestionListOptions{}
			if all {
				opts.Limit = -1 // SDK treats -1 as "fetch all"
			} else if limit > 0 {
				opts.Limit = limit
			}
			if page > 0 {
				opts.Page = page
			}

			questionsResult, err := app.Account().Checkins().ListQuestions(cmd.Context(), qID, opts)
			if err != nil {
				return convertSDKError(err)
			}
			questions := questionsResult.Questions

			return app.OK(questions,
				output.WithSummary(fmt.Sprintf("%d check-in questions", len(questions))),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question <id> --in %s", resolvedProjectID),
						Description: "View question details",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers <question_id> --in %s", resolvedProjectID),
						Description: "View answers",
					},
				),
			)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of questions to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all questions (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")

	return cmd
}

func newCheckinsQuestionCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "question <id|url>",
		Short: "Show or manage a question",
		Args:  cobra.MaximumNArgs(1),
	}

	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Show help when invoked with no arguments
		if len(args) == 0 {
			return missingArg(cmd, "<id|url>")
		}
		return runCheckinsQuestionShow(cmd, *project, args[0], cf)
	}

	cmd.AddCommand(
		newCheckinsQuestionShowCmd(project),
		newCheckinsQuestionCreateCmd(project),
		newCheckinsQuestionUpdateCmd(project),
	)

	return cmd
}

func newCheckinsQuestionShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show question details",
		Long: `Display details of a check-in question.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins question show 789 --in my-project
  basecamp checkins question show https://3.basecamp.com/123/buckets/456/questionnaires/questions/789`,
		Args: cobra.ExactArgs(1),
	}

	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runCheckinsQuestionShow(cmd, *project, args[0], cf)
	}

	return cmd
}

func runCheckinsQuestionShow(cmd *cobra.Command, project, questionIDStr string, cf *commentFlags) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	questionIDStr, urlProjectID := extractWithProject(questionIDStr)

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid question ID")
	}

	question, err := app.Account().Checkins().GetQuestion(cmd.Context(), questionID)
	if err != nil {
		return convertSDKError(err)
	}

	enrichment := fetchCommentsForRecording(cmd.Context(), app, questionIDStr, cf)
	data, commentOpts := enrichment.apply(question, "")
	summary := fmt.Sprintf("%s (%d answers)", question.Title, question.AnswersCount)

	opts := make([]output.ResponseOption, 0, 2+len(commentOpts))
	opts = append(opts,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionIDStr, resolvedProjectID),
				Description: "View answers",
			},
			output.Breadcrumb{
				Action:      "questions",
				Cmd:         fmt.Sprintf("basecamp checkins questions --in %s", resolvedProjectID),
				Description: "View all questions",
			},
		),
	)
	opts = append(opts, commentOpts...)

	return app.OK(data, opts...)
}

func newCheckinsQuestionCreateCmd(project *string) *cobra.Command {
	var questionnaireID string
	var frequency string
	var timeOfDay string
	var days string
	var visibleToClients bool

	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a new check-in question",
		Long: `Create a new check-in question.

Frequency options: every_day, every_week, every_other_week, every_month, on_certain_days
Days format: comma-separated (0=Sun, 1=Mon, 2=Tue, 3=Wed, 4=Thu, 5=Fri, 6=Sat)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<title>")
			}

			title := args[0]

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			// Get questionnaire ID
			resolvedQuestionnaireID := questionnaireID
			if resolvedQuestionnaireID == "" {
				resolvedQuestionnaireID, err = getQuestionnaireID(cmd, app, resolvedProjectID)
				if err != nil {
					return err
				}
			}

			qID, err := strconv.ParseInt(resolvedQuestionnaireID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid questionnaire ID")
			}

			// Default values
			if frequency == "" {
				frequency = "every_day"
			}
			if days == "" {
				days = "1,2,3,4,5"
			}

			// Parse days into array of ints
			dayParts := strings.Split(days, ",")
			daysArray := make([]int, 0, len(dayParts))
			for _, d := range dayParts {
				d = strings.TrimSpace(d)
				if d != "" {
					dayInt, err := strconv.Atoi(d)
					if err != nil {
						return output.ErrUsage("Invalid day value: " + d)
					}
					daysArray = append(daysArray, dayInt)
				}
			}

			// Parse time of day (default 5:00pm = 17:00)
			hour := 17
			minute := 0
			if timeOfDay != "" {
				hour, minute, err = parseTimeOfDay(timeOfDay)
				if err != nil {
					return output.ErrUsage("Invalid time format: " + timeOfDay)
				}
			}

			req := &basecamp.CreateQuestionRequest{
				Title: title,
				Schedule: &basecamp.QuestionSchedule{
					Frequency: frequency,
					Days:      daysArray,
					Hour:      &hour,
					Minute:    &minute,
				},
			}

			// Set client visibility only when the flag was provided. Omitting it
			// uses the server's default: team-only when posting as a team member,
			// but a client-authenticated caller always creates client-visible
			// records (an explicit false is overridden server-side).
			if cmd.Flags().Changed("visible-to-clients") {
				req.VisibleToClients = &visibleToClients
			}

			question, err := app.Account().Checkins().CreateQuestion(cmd.Context(), qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(question,
				output.WithSummary(fmt.Sprintf("Created question #%d: %s", question.ID, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question %d --in %s", question.ID, resolvedProjectID),
						Description: "View question",
					},
					output.Breadcrumb{
						Action:      "questions",
						Cmd:         fmt.Sprintf("basecamp checkins questions --in %s", resolvedProjectID),
						Description: "View all questions",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&questionnaireID, "questionnaire", "", "Questionnaire ID (auto-detected)")
	cmd.Flags().StringVarP(&frequency, "frequency", "f", "", "Schedule frequency (default: every_day)")
	cmd.Flags().StringVar(&timeOfDay, "time", "", "Time to ask (default: 5:00pm)")
	cmd.Flags().StringVarP(&days, "days", "d", "", "Days to ask, comma-separated (default: 1,2,3,4,5)")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the question visible to clients on the project (omit for the server default; client-authenticated callers always post client-visible)")

	return cmd
}

func newCheckinsQuestionUpdateCmd(project *string) *cobra.Command {
	var frequency string
	var timeOfDay string
	var days string

	cmd := &cobra.Command{
		Use:   "update <id|url> [title]",
		Short: "Update a check-in question",
		Long: `Update a check-in question's title or schedule.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins question update 789 "new question" --in my-project
  basecamp checkins question update 789 --frequency every_week --in my-project`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show help when invoked with no arguments
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}

			// Extract ID and project from URL if provided
			questionIDStr, urlProjectID := extractWithProject(args[0])

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			title := ""
			if len(args) > 1 {
				title = args[1]
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}

			// Build request
			req := &basecamp.UpdateQuestionRequest{}
			if title != "" {
				req.Title = title
			}

			if frequency != "" || timeOfDay != "" || days != "" {
				schedule := &basecamp.QuestionSchedule{}
				if frequency != "" {
					schedule.Frequency = frequency
				}
				if timeOfDay != "" {
					hour, minute, err := parseTimeOfDay(timeOfDay)
					if err != nil {
						return output.ErrUsage("Invalid time format: " + timeOfDay)
					}
					schedule.Hour = &hour
					schedule.Minute = &minute
				}
				if days != "" {
					dayParts := strings.Split(days, ",")
					daysArray := make([]int, 0, len(dayParts))
					for _, d := range dayParts {
						d = strings.TrimSpace(d)
						if d != "" {
							dayInt, err := strconv.Atoi(d)
							if err != nil {
								return output.ErrUsage("Invalid day value: " + d)
							}
							daysArray = append(daysArray, dayInt)
						}
					}
					schedule.Days = daysArray
				}
				req.Schedule = schedule
			}

			if req.Title == "" && req.Schedule == nil {
				return noChanges(cmd)
			}

			question, err := app.Account().Checkins().UpdateQuestion(cmd.Context(), questionID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(question,
				output.WithSummary(fmt.Sprintf("Updated question #%s: %s", questionIDStr, question.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "question",
						Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionIDStr, resolvedProjectID),
						Description: "View question",
					},
				),
			)
		},
	}

	cmd.Flags().StringVarP(&frequency, "frequency", "f", "", "New schedule frequency")
	cmd.Flags().StringVar(&timeOfDay, "time", "", "New time to ask")
	cmd.Flags().StringVarP(&days, "days", "d", "", "New days to ask")

	return cmd
}

func newCheckinsAnswersCmd(project, questionnaireID *string) *cobra.Command {
	var limit int
	var page int
	var all bool
	var allProjects bool
	var by string

	cmd := &cobra.Command{
		Use:   "answers [question_id|url]",
		Short: "List answers for a question, or across every project",
		Long: `List answers for a check-in question.

You can pass either a question ID or a Basecamp URL:
  basecamp checkins answers 789 --in my-project
  basecamp checkins answers https://3.basecamp.com/123/buckets/456/questions/789

Use --by to filter answers by a specific person (name, email, ID, or "me"):
  basecamp checkins answers 789 --by me --in my-project
  basecamp checkins answers 789 --by "Alice Smith" --in my-project

Omit the question ID to list check-in answers across every project you can
reach, newest first. A configured project is ignored there, because a project
on its own cannot name a question; --all-projects states that intent outright:
  basecamp checkins answers
  basecamp checkins answers --all-projects`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Pick the scope before validating anything against it: the
			// account-wide feed serves any positive page, while the
			// per-question listing only serves page 1.
			//
			// Answers belong to one question, so a project can never select
			// this listing on its own. That makes an explicit project without
			// a question ID a request we cannot answer, and a merely
			// configured project something to ignore rather than honor.
			explicitProject := *project != "" || app.Flags.Project != ""

			switch {
			case len(args) > 0 && allProjects:
				return output.ErrUsageHint("--all-projects cannot be combined with a question ID",
					"Drop the ID to list answers across every project, or drop --all-projects to list that question's answers")
			case len(args) > 0:
				// A question ID already names its questionnaire, so
				// --questionnaire cannot narrow anything here. It was
				// accepted and dropped on this branch; say so instead.
				if *questionnaireID != "" {
					return output.ErrUsageHint(
						"--questionnaire cannot narrow a question's answers",
						"The question ID already selects its questionnaire. Drop --questionnaire.")
				}
				return runCheckinsAnswers(cmd, app, *project, args[0], by, limit, page, all)
			case explicitProject && allProjects:
				return output.ErrUsageHint("--all-projects cannot be combined with --project",
					"Drop --project to list answers across every project")
			case explicitProject:
				return output.ErrUsageHint("A project alone cannot select check-in answers",
					"Pass a question ID: basecamp checkins answers <question_id>")
			}

			return runCheckinsAnswersAccountWide(cmd, app, *questionnaireID, limit, page, all)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "Maximum number of answers to fetch (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "Fetch all answers (no limit)")
	cmd.Flags().IntVar(&page, "page", 0, "Fetch a single page (use --all for everything)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "List check-in answers across every project (no question ID)")
	cmd.Flags().StringVar(&by, "by", "", "Filter answers by person (name, email, ID, or \"me\")")

	return cmd
}

// runCheckinsAnswers lists the answers to a single question.
func runCheckinsAnswers(cmd *cobra.Command, app *appctx.App, project, questionArg, by string, limit, page int, all bool) error {
	// Validate flag combinations
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}
	if page > 1 {
		return output.ErrUsage("only --page 1 is supported; use --all to fetch everything")
	}

	// Extract ID and project from URL if provided
	questionIDStr, urlProjectID := extractWithProject(questionArg)

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	questionID, err := strconv.ParseInt(questionIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid question ID")
	}

	// Build pagination options
	opts := &basecamp.AnswerListOptions{}
	if all {
		opts.Limit = -1 // SDK treats -1 as "fetch all"
	} else if limit > 0 {
		opts.Limit = limit
	}
	if page > 0 {
		opts.Page = page
	}

	trimmedBy := strings.TrimSpace(by)
	if cmd.Flags().Changed("by") && trimmedBy == "" {
		return output.ErrUsage("--by value cannot be blank")
	}

	var answers []basecamp.QuestionAnswer
	if trimmedBy != "" {
		personIDStr, _, err := app.Names.ResolvePerson(cmd.Context(), trimmedBy)
		if err != nil {
			return err
		}
		personID, err := strconv.ParseInt(personIDStr, 10, 64)
		if err != nil {
			return output.ErrUsage("Invalid person ID")
		}
		answersResult, err := app.Account().Checkins().ListAnswersByPerson(cmd.Context(), questionID, personID, opts)
		if err != nil {
			return convertSDKError(err)
		}
		answers = answersResult.Answers
	} else {
		answersResult, err := app.Account().Checkins().ListAnswers(cmd.Context(), questionID, opts)
		if err != nil {
			return convertSDKError(err)
		}
		answers = answersResult.Answers
	}

	return app.OK(answers,
		output.WithSummary(fmt.Sprintf("%d answers", len(answers))),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "answer",
				Cmd:         fmt.Sprintf("basecamp checkins answer <id> --in %s", resolvedProjectID),
				Description: "View answer details",
			},
			output.Breadcrumb{
				Action:      "question",
				Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionIDStr, resolvedProjectID),
				Description: "View question",
			},
		),
	)
}

// runCheckinsAnswersAccountWide lists check-in answers across every project the
// account can reach. The listing is a flat []Recording, the same payload
// `recordings list` already hands the styled renderer, so it needs no
// format-dependent flattening.
func runCheckinsAnswersAccountWide(cmd *cobra.Command, app *appctx.App, questionnaireID string, limit, page int, all bool) error {
	if err := rejectAccountWideTodolist(app, "check-in answer"); err != nil {
		return err
	}
	// A questionnaire is a container inside one project and a person filter
	// has no aggregate endpoint, so neither can narrow this feed. Accepting
	// either would answer a different question than the one asked.
	if questionnaireID != "" {
		return output.ErrUsageHint("--questionnaire names a check-in container inside one project",
			"Pass a question ID to list that question's answers, or drop --questionnaire")
	}
	if cmd.Flags().Changed("by") {
		return output.ErrUsageHint("--by has no account-wide equivalent",
			"Filter by person within one question: basecamp checkins answers <question_id> --by <person>")
	}

	if limit < 0 {
		return output.ErrUsage("--limit cannot be negative")
	}
	if page < 0 {
		return output.ErrUsage("--page cannot be negative")
	}
	if cmd.Flags().Changed("page") && page == 0 {
		return output.ErrUsage("--page 0 is not a page; use --all to fetch every page")
	}
	if all && limit > 0 {
		return output.ErrUsage("--all and --limit are mutually exclusive")
	}
	if page > 0 && (all || limit > 0) {
		return output.ErrUsage("--page cannot be combined with --all or --limit")
	}

	// The project-scoped default is "0 = all", so the account-wide default
	// follows every page too. Only an explicit --page narrows it to one.
	answersPage, err := app.Account().Everything().Checkins(cmd.Context(), accountWidePage(page, all || page == 0))
	if err != nil {
		return convertSDKError(err)
	}

	answers := answersPage.Recordings
	fetched := len(answers)
	if limit > 0 && fetched > limit {
		answers = answers[:limit]
	}

	respOpts := accountWideRespOpts(len(answers), "check-in answer", "check-in answers", answersPage.Meta, "--all", limit > 0)
	respOpts = append(respOpts, output.WithDisplayData(flattenAccountWideRecordings(answers)))
	if len(answers) < fetched {
		respOpts = append(respOpts, output.WithNotice(fmt.Sprintf(
			"Showing %d of %d fetched check-in answers (--limit); drop --limit for all of them", len(answers), fetched)))
	}
	respOpts = append(respOpts, output.WithBreadcrumbs(
		output.Breadcrumb{
			Action:      "answer",
			Cmd:         "basecamp checkins answer <id>",
			Description: "View answer details",
		},
		output.Breadcrumb{
			Action:      "answers",
			Cmd:         "basecamp checkins answers <question_id>",
			Description: "View one question's answers",
		},
	))

	return app.OK(answers, respOpts...)
}

func newCheckinsAnswerCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "answer <id|url>",
		Short: "Show or manage an answer",
		Args:  cobra.MaximumNArgs(1),
	}

	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Show help when invoked with no arguments
		if len(args) == 0 {
			return missingArg(cmd, "<id|url>")
		}
		return runCheckinsAnswerShow(cmd, *project, args[0], cf)
	}

	cmd.AddCommand(
		newCheckinsAnswerShowCmd(project),
		newCheckinsAnswerCreateCmd(project),
		newCheckinsAnswerUpdateCmd(project),
	)

	return cmd
}

func newCheckinsAnswerShowCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id|url>",
		Short: "Show answer details",
		Long: `Display details of a check-in answer.

You can pass either an answer ID or a Basecamp URL:
  basecamp checkins answer show 789 --in my-project
  basecamp checkins answer show https://3.basecamp.com/123/buckets/456/question_answers/789`,
		Args: cobra.ExactArgs(1),
	}

	cf := addCommentFlags(cmd, false)

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runCheckinsAnswerShow(cmd, *project, args[0], cf)
	}

	return cmd
}

func runCheckinsAnswerShow(cmd *cobra.Command, project, answerIDStr string, cf *commentFlags) error {
	app := appctx.FromContext(cmd.Context())

	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	// Extract ID and project from URL if provided
	answerIDStr, urlProjectID := extractWithProject(answerIDStr)

	// Resolve project - use URL > flag > config, with interactive fallback
	projectID := project
	if projectID == "" && urlProjectID != "" {
		projectID = urlProjectID
	}
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return err
	}

	answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
	if err != nil {
		return output.ErrUsage("Invalid answer ID")
	}

	answer, err := app.Account().Checkins().GetAnswer(cmd.Context(), answerID)
	if err != nil {
		return convertSDKError(err)
	}

	author := "Unknown"
	if answer.Creator != nil && answer.Creator.Name != "" {
		author = answer.Creator.Name
	}
	date := answer.GroupOn
	if len(date) > 10 {
		date = date[:10]
	}

	enrichment := fetchCommentsForRecording(cmd.Context(), app, answerIDStr, cf)
	data, commentOpts := enrichment.apply(answer, "")
	summary := fmt.Sprintf("Answer by %s on %s", author, date)

	questionID := ""
	if answer.Parent != nil {
		questionID = strconv.FormatInt(answer.Parent.ID, 10)
	}

	opts := make([]output.ResponseOption, 0, 2+len(commentOpts))
	opts = append(opts,
		output.WithSummary(summary),
		output.WithBreadcrumbs(
			output.Breadcrumb{
				Action:      "question",
				Cmd:         fmt.Sprintf("basecamp checkins question %s --in %s", questionID, resolvedProjectID),
				Description: "View question",
			},
			output.Breadcrumb{
				Action:      "answers",
				Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
				Description: "View all answers",
			},
		),
	)
	opts = append(opts, commentOpts...)

	return app.OK(data, opts...)
}

func newCheckinsAnswerCreateCmd(project *string) *cobra.Command {
	var groupOn string
	var attachFiles []string

	cmd := &cobra.Command{
		Use:   "create <question-id> <content>",
		Short: "Create an answer to a question",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<question-id>")
			}
			if len(args) < 2 {
				return missingArg(cmd, "<content>")
			}

			questionID := args[0]
			content := strings.Join(args[1:], " ")

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project, with interactive fallback
			projectID := *project
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			qID, err := strconv.ParseInt(questionID, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid question ID")
			}
			effectiveGroupOn := groupOn
			if effectiveGroupOn == "" {
				effectiveGroupOn = checkinsNow().Format("2006-01-02")
			}

			html := richtext.MarkdownToHTML(content)

			// Resolve inline images
			html, imgErr := resolveLocalImages(cmd, app, html)
			if imgErr != nil {
				return imgErr
			}

			// Upload explicit --attach files and embed
			if len(attachFiles) > 0 {
				refs, attachErr := uploadAttachments(cmd, app, attachFiles)
				if attachErr != nil {
					return attachErr
				}
				html = richtext.EmbedAttachments(html, refs)
			}

			req := &basecamp.CreateAnswerRequest{
				Content: html,
				GroupOn: effectiveGroupOn,
			}

			answer, err := app.Account().Checkins().CreateAnswer(cmd.Context(), qID, req)
			if err != nil {
				return convertSDKError(err)
			}

			author := "You"
			if answer.Creator != nil && answer.Creator.Name != "" {
				author = answer.Creator.Name
			}

			return app.OK(answer,
				output.WithSummary(fmt.Sprintf("Answer created by %s", author)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("basecamp checkins answer %d --in %s", answer.ID, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
						Description: "View all answers",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&groupOn, "date", "", "Date to group answer (ISO 8601, e.g., 2024-01-22; defaults to today)")
	cmd.Flags().StringArrayVar(&attachFiles, "attach", nil, "Attach file (repeatable)")

	return cmd
}

func newCheckinsAnswerUpdateCmd(project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update <id|url> <content>",
		Short: "Update an answer",
		Long: `Update an existing check-in answer.

You can pass either an answer ID or a Basecamp URL:
  basecamp checkins answer update 789 "updated answer" --in my-project`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return missingArg(cmd, "<id|url>")
			}
			if len(args) < 2 {
				return missingArg(cmd, "<content>")
			}

			// Extract ID and project from URL if provided
			answerIDStr, urlProjectID := extractWithProject(args[0])

			content := strings.Join(args[1:], " ")

			app := appctx.FromContext(cmd.Context())

			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			// Resolve project - use URL > flag > config, with interactive fallback
			projectID := *project
			if projectID == "" && urlProjectID != "" {
				projectID = urlProjectID
			}
			if projectID == "" {
				projectID = app.Flags.Project
			}
			if projectID == "" {
				projectID = app.Config.ProjectID
			}
			if projectID == "" {
				if err := ensureProject(cmd, app); err != nil {
					return err
				}
				projectID = app.Config.ProjectID
			}

			resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
			if err != nil {
				return err
			}

			answerID, err := strconv.ParseInt(answerIDStr, 10, 64)
			if err != nil {
				return output.ErrUsage("Invalid answer ID")
			}

			answerHTML := richtext.MarkdownToHTML(content)
			answerHTML, resolveErr := resolveLocalImages(cmd, app, answerHTML)
			if resolveErr != nil {
				return resolveErr
			}

			req := &basecamp.UpdateAnswerRequest{
				Content: answerHTML,
			}

			err = app.Account().Checkins().UpdateAnswer(cmd.Context(), answerID, req)
			if err != nil {
				return convertSDKError(err)
			}

			// Fetch the updated answer for display
			answer, err := app.Account().Checkins().GetAnswer(cmd.Context(), answerID)
			if err != nil {
				return convertSDKError(err)
			}

			questionID := ""
			if answer.Parent != nil {
				questionID = strconv.FormatInt(answer.Parent.ID, 10)
			}

			return app.OK(answer,
				output.WithSummary("Answer updated"),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "answer",
						Cmd:         fmt.Sprintf("basecamp checkins answer %s --in %s", answerIDStr, resolvedProjectID),
						Description: "View answer",
					},
					output.Breadcrumb{
						Action:      "answers",
						Cmd:         fmt.Sprintf("basecamp checkins answers %s --in %s", questionID, resolvedProjectID),
						Description: "View all answers",
					},
				),
			)
		},
	}

	return cmd
}

// getQuestionnaireID retrieves the questionnaire ID from a project's dock, handling multi-dock projects.
func getQuestionnaireID(cmd *cobra.Command, app *appctx.App, projectID string) (string, error) {
	return getDockToolID(cmd.Context(), app, projectID, "questionnaire", "", "questionnaire", "questionnaire")
}

// parseTimeOfDay parses a time string like "5:00pm" or "17:00" and returns hour and minute.
func parseTimeOfDay(t string) (int, int, error) {
	t = strings.ToLower(strings.TrimSpace(t))

	// Handle 24-hour format
	if strings.Contains(t, ":") && !strings.Contains(t, "am") && !strings.Contains(t, "pm") {
		parts := strings.Split(t, ":")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid time format")
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, err
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
		return hour, minute, nil
	}

	// Handle 12-hour format with am/pm
	isPM := strings.Contains(t, "pm")
	t = strings.TrimSuffix(t, "am")
	t = strings.TrimSuffix(t, "pm")
	t = strings.TrimSpace(t)

	parts := strings.Split(t, ":")
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}

	minute := 0
	if len(parts) > 1 {
		minute, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}

	if isPM && hour != 12 {
		hour += 12
	} else if !isPM && hour == 12 {
		hour = 0
	}

	return hour, minute, nil
}
