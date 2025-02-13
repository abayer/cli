// Copyright © 2020 The Tekton Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustertask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/ghodss/yaml"
	"github.com/spf13/cobra"
	"github.com/tektoncd/cli/pkg/cli"
	ctactions "github.com/tektoncd/cli/pkg/clustertask"
	"github.com/tektoncd/cli/pkg/cmd/pipelineresource"
	"github.com/tektoncd/cli/pkg/cmd/taskrun"
	"github.com/tektoncd/cli/pkg/file"
	"github.com/tektoncd/cli/pkg/flags"
	"github.com/tektoncd/cli/pkg/formatted"
	"github.com/tektoncd/cli/pkg/labels"
	"github.com/tektoncd/cli/pkg/options"
	"github.com/tektoncd/cli/pkg/params"
	"github.com/tektoncd/cli/pkg/pods"
	tractions "github.com/tektoncd/cli/pkg/taskrun"
	"github.com/tektoncd/cli/pkg/workspaces"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
)

var (
	errNoClusterTask             = errors.New("missing ClusterTask name")
	errInvalidClusterTask        = "ClusterTask name %s does not exist"
	errClusterTaskAlreadyPresent = "ClusterTask with name %s already exists"
)

const invalidResource = "invalid input format for resource parameter: "

type startOptions struct {
	cliparams          cli.Params
	stream             *cli.Stream
	Params             []string
	InputResources     []string
	OutputResources    []string
	ServiceAccountName string
	Last               bool
	Labels             []string
	ShowLog            bool
	TimeOut            string
	DryRun             bool
	Output             string
	PrefixName         string
	Workspaces         []string
	UseParamDefaults   bool
	clustertask        *v1beta1.ClusterTask
	askOpts            survey.AskOpt
	TektonOptions      flags.TektonOptions
	PodTemplate        string
	UseTaskRun         string
}

// NameArg validates that the first argument is a valid clustertask name
func NameArg(args []string, p cli.Params, opt *startOptions) error {
	if len(args) == 0 {
		return errNoClusterTask
	}

	c, err := p.Clients()
	if err != nil {
		return err
	}

	name := args[0]
	ct, err := ctactions.Get(c, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf(errInvalidClusterTask, name)
	}
	opt.clustertask = ct
	if ct.Spec.Params != nil {
		params.FilterParamsByType(ct.Spec.Params)
	}

	return nil
}

func startCommand(p cli.Params) *cobra.Command {
	opt := startOptions{
		cliparams: p,
		askOpts: func(opt *survey.AskOptions) error {
			opt.Stdio = terminal.Stdio{
				In:  os.Stdin,
				Out: os.Stdout,
				Err: os.Stderr,
			}
			return nil
		},
	}

	eg := `Start ClusterTask foo by creating a TaskRun named "foo-run-xyz123" in namespace 'bar':

    tkn clustertask start foo -n bar

or

    tkn ct start foo -n bar

For params value, if you want to provide multiple values, provide them comma separated
like cat,foo,bar

For passing the workspaces via flags:

- In case of emptyDir, you can pass it like -w name=my-empty-dir,emptyDir=
- In case of configMap, you can pass it like -w name=my-config,config=rpg,item=ultimav=1
- In case of secrets, you can pass it like -w name=my-secret,secret=secret-name
- In case of pvc, you can pass it like -w name=my-pvc,claimName=pvc1
- In case of volumeClaimTemplate, you can pass it like -w name=my-volume-claim-template,volumeClaimTemplateFile=workspace-template.yaml
  but before you need to create a workspace-template.yaml file. Sample contents of the file are as follows:
  spec:
   accessModes:
     - ReadWriteOnce
   resources:
     requests:
       storage: 1Gi
`

	c := &cobra.Command{
		Use:   "start",
		Short: "Start ClusterTasks",
		Annotations: map[string]string{
			"commandType": "main",
		},
		Example:           eg,
		SilenceUsage:      true,
		ValidArgsFunction: formatted.ParentCompletion,
		Args: func(cmd *cobra.Command, args []string) error {
			if opt.UseParamDefaults && (opt.Last || opt.UseTaskRun != "") {
				return errors.New("cannot use --last or --use-taskrun options with --use-param-defaults option")
			}
			format := strings.ToLower(opt.Output)
			if format != "" && format != "json" && format != "yaml" {
				return fmt.Errorf("output format specified is %s but must be yaml or json", opt.Output)
			}
			if format != "" && opt.ShowLog {
				return errors.New("cannot use --output option with --showlog option")
			}
			if err := flags.InitParams(p, cmd); err != nil {
				return err
			}
			return NameArg(args, p, &opt)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opt.stream = &cli.Stream{
				Out: cmd.OutOrStdout(),
				Err: cmd.OutOrStderr(),
			}
			if opt.Last && opt.UseTaskRun != "" {
				return fmt.Errorf("using --last and --use-taskrun are not compatible")
			}
			opt.TektonOptions = flags.GetTektonOptions(cmd)
			return startClusterTask(opt, args)
		},
	}

	c.Flags().StringSliceVarP(&opt.InputResources, "inputresource", "i", []string{}, "pass the input resource name and ref as name=ref")
	c.Flags().StringSliceVarP(&opt.OutputResources, "outputresource", "o", []string{}, "pass the output resource name and ref as name=ref")
	c.Flags().StringArrayVarP(&opt.Params, "param", "p", []string{}, "pass the param as key=value for string type, or key=value1,value2,... for array type")
	c.Flags().StringVarP(&opt.ServiceAccountName, "serviceaccount", "s", "", "pass the serviceaccount name")
	c.Flags().BoolVarP(&opt.Last, "last", "L", false, "re-run the ClusterTask using last TaskRun values")
	c.Flags().StringVarP(&opt.UseTaskRun, "use-taskrun", "", "", "specify a TaskRun name to use its values to re-run the TaskRun")
	c.Flags().StringSliceVarP(&opt.Labels, "labels", "l", []string{}, "pass labels as label=value.")
	c.Flags().StringArrayVarP(&opt.Workspaces, "workspace", "w", []string{}, "pass one or more workspaces to map to the corresponding physical volumes")
	c.Flags().BoolVarP(&opt.ShowLog, "showlog", "", false, "show logs right after starting the ClusterTask")
	c.Flags().StringVar(&opt.TimeOut, "timeout", "", "timeout for TaskRun")
	c.Flags().BoolVarP(&opt.DryRun, "dry-run", "", false, "preview TaskRun without running it")
	c.Flags().StringVarP(&opt.Output, "output", "", "", "format of TaskRun (yaml or json)")
	c.Flags().StringVarP(&opt.PrefixName, "prefix-name", "", "", "specify a prefix for the TaskRun name (must be lowercase alphanumeric characters)")
	c.Flags().StringVar(&opt.PodTemplate, "pod-template", "", "local or remote file containing a PodTemplate definition")
	c.Flags().BoolVar(&opt.UseParamDefaults, "use-param-defaults", false, "use default parameter values without prompting for input")

	return c
}

func startClusterTask(opt startOptions, args []string) error {
	tr := &v1beta1.TaskRun{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "tekton.dev/v1beta1",
			Kind:       "TaskRun",
		},
		ObjectMeta: metav1.ObjectMeta{},
	}

	cs, err := opt.cliparams.Clients()
	if err != nil {
		return err
	}

	ctname := args[0]
	tr.Spec = v1beta1.TaskRunSpec{
		TaskRef: &v1beta1.TaskRef{
			Name: ctname,
			Kind: v1beta1.ClusterTaskKind, // Specify TaskRun is for a ClusterTask kind
		},
	}

	if err := opt.getInputs(); err != nil {
		return err
	}

	if opt.Last || opt.UseTaskRun != "" {
		taskRunOpts := options.TaskRunOpts{
			CliParams:  opt.cliparams,
			Last:       opt.Last,
			UseTaskRun: opt.UseTaskRun,
			PrefixName: opt.PrefixName,
		}
		err := taskRunOpts.UseTaskRunFrom(tr, cs, ctname, "ClusterTask")
		if err != nil {
			return err
		}
	}

	if opt.PrefixName == "" && !opt.Last && opt.UseTaskRun == "" {
		tr.ObjectMeta.GenerateName = ctname + "-run-"
	} else if opt.PrefixName != "" {
		tr.ObjectMeta.GenerateName = opt.PrefixName + "-"
	}

	if opt.TimeOut != "" {
		timeoutDuration, err := time.ParseDuration(opt.TimeOut)
		if err != nil {
			return err
		}
		tr.Spec.Timeout = &metav1.Duration{Duration: timeoutDuration}
	}

	if tr.Spec.Resources == nil {
		tr.Spec.Resources = &v1beta1.TaskRunResources{}
	}
	inputRes, err := mergeRes(tr.Spec.Resources.Inputs, opt.InputResources)
	if err != nil {
		return err
	}
	tr.Spec.Resources.Inputs = inputRes

	outRes, err := mergeRes(tr.Spec.Resources.Outputs, opt.OutputResources)
	if err != nil {
		return err
	}
	tr.Spec.Resources.Outputs = outRes

	labels, err := labels.MergeLabels(tr.ObjectMeta.Labels, opt.Labels)
	if err != nil {
		return err
	}
	tr.ObjectMeta.Labels = labels

	workspaces, err := workspaces.Merge(tr.Spec.Workspaces, opt.Workspaces, cs.HTTPClient)
	if err != nil {
		return err
	}
	tr.Spec.Workspaces = workspaces

	param, err := params.MergeParam(tr.Spec.Params, opt.Params)
	if err != nil {
		return err
	}
	tr.Spec.Params = param

	if len(opt.ServiceAccountName) > 0 {
		tr.Spec.ServiceAccountName = opt.ServiceAccountName
	}

	podTemplateLocation := opt.PodTemplate
	if podTemplateLocation != "" {
		podTemplate, err := pods.ParsePodTemplate(cs.HTTPClient, podTemplateLocation, file.IsYamlFile(), fmt.Errorf("invalid file format for %s: .yaml or .yml file extension and format required", podTemplateLocation))
		if err != nil {
			return err
		}
		tr.Spec.PodTemplate = &podTemplate
	}

	if opt.DryRun {
		return printTaskRun(cs, opt.Output, opt.stream, tr)
	}

	trCreated, err := tractions.Create(cs, tr, metav1.CreateOptions{}, opt.cliparams.Namespace())
	if err != nil {
		return err
	}

	if opt.Output != "" {
		return printTaskRun(cs, opt.Output, opt.stream, trCreated)
	}

	fmt.Fprintf(opt.stream.Out, "TaskRun started: %s\n", trCreated.Name)
	if !opt.ShowLog {
		inOrderString := "\nIn order to track the TaskRun progress run:\ntkn taskrun "
		if opt.TektonOptions.Context != "" {
			inOrderString += fmt.Sprintf("--context=%s ", opt.TektonOptions.Context)
		}
		inOrderString += fmt.Sprintf("logs %s -f -n %s\n", trCreated.Name, trCreated.Namespace)

		fmt.Fprint(opt.stream.Out, inOrderString)
		return nil
	}

	fmt.Fprintf(opt.stream.Out, "Waiting for logs to be available...\n")
	runLogOpts := &options.LogOptions{
		TaskrunName: trCreated.Name,
		Stream:      opt.stream,
		Follow:      true,
		Prefixing:   true,
		Params:      opt.cliparams,
		AllSteps:    false,
	}
	return taskrun.Run(runLogOpts)
}

func mergeRes(r []v1beta1.TaskResourceBinding, optRes []string) ([]v1beta1.TaskResourceBinding, error) {
	res, err := parseRes(optRes)
	if err != nil {
		return nil, err
	}

	if len(res) == 0 {
		return r, nil
	}

	for i := range r {
		if v, ok := res[r[i].Name]; ok {
			r[i] = v
			delete(res, v.Name)
		}
	}
	for _, v := range res {
		r = append(r, v)
	}
	sort.Slice(r, func(i, j int) bool { return r[i].Name < r[j].Name })
	return r, nil
}

func parseRes(res []string) (map[string]v1beta1.TaskResourceBinding, error) {
	resources := map[string]v1beta1.TaskResourceBinding{}
	for _, v := range res {
		r := strings.SplitN(v, "=", 2)
		if len(r) != 2 {
			return nil, errors.New(invalidResource + v)
		}
		resources[r[0]] = v1beta1.TaskResourceBinding{
			PipelineResourceBinding: v1beta1.PipelineResourceBinding{
				Name: r[0],
				ResourceRef: &v1beta1.PipelineResourceRef{
					Name: r[1],
				},
			},
		}
	}
	return resources, nil
}

func printTaskRun(c *cli.Clients, output string, s *cli.Stream, tr *v1beta1.TaskRun) error {
	trWithVersion, err := convertedTrVersion(c, tr)
	if err != nil {
		return err
	}
	format := strings.ToLower(output)

	if format == "" || format == "yaml" {
		trBytes, err := yaml.Marshal(trWithVersion)
		if err != nil {
			return err
		}
		fmt.Fprintf(s.Out, "%s", trBytes)
	}

	if format == "json" {
		trBytes, err := json.MarshalIndent(trWithVersion, "", "\t")
		if err != nil {
			return err
		}
		fmt.Fprintf(s.Out, "%s\n", trBytes)
	}

	return nil
}

func getAPIVersion(discovery discovery.DiscoveryInterface) (string, error) {
	_, err := discovery.ServerResourcesForGroupVersion("tekton.dev/v1beta1")
	if err != nil {
		_, err = discovery.ServerResourcesForGroupVersion("tekton.dev/v1alpha1")
		if err != nil {
			return "", fmt.Errorf("couldn't get available Tekton api versions from server")
		}
		return "tekton.dev/v1alpha1", nil
	}
	return "tekton.dev/v1beta1", nil
}

func convertedTrVersion(c *cli.Clients, tr *v1beta1.TaskRun) (interface{}, error) {
	version, err := getAPIVersion(c.Tekton.Discovery())
	if err != nil {
		return nil, err
	}

	if version == "tekton.dev/v1alpha1" {
		trConverted := tractions.ConvertFrom(tr)
		trConverted.APIVersion = version
		trConverted.Kind = "TaskRun"
		if err != nil {
			return nil, err
		}
		return &trConverted, nil
	}

	return tr, nil
}

func (opt *startOptions) getInputs() error {
	intOpts := options.InteractiveOpts{
		Stream:    opt.stream,
		CliParams: opt.cliparams,
		AskOpts:   opt.askOpts,
		Ns:        opt.cliparams.Namespace(),
	}

	if opt.clustertask.Spec.Resources != nil && !opt.Last && opt.UseTaskRun == "" {
		if len(opt.InputResources) == 0 {
			if err := intOpts.ClusterTaskInputResources(opt.clustertask, createPipelineResource); err != nil {
				return err
			}
			opt.InputResources = append(opt.InputResources, intOpts.InputResources...)
		}
		if len(opt.OutputResources) == 0 {
			if err := intOpts.ClusterTaskOutputResources(opt.clustertask, createPipelineResource); err != nil {
				return err
			}
			opt.OutputResources = append(opt.OutputResources, intOpts.OutputResources...)
		}
	}

	params.FilterParamsByType(opt.clustertask.Spec.Params)
	if !opt.Last && opt.UseTaskRun == "" {
		skipParams, err := params.ParseParams(opt.Params)
		if err != nil {
			return err
		}
		if err := intOpts.ClusterTaskParams(opt.clustertask, skipParams, opt.UseParamDefaults); err != nil {
			return err
		}
		opt.Params = append(opt.Params, intOpts.Params...)
	}

	if len(opt.Workspaces) == 0 && !opt.Last && opt.UseTaskRun == "" {
		if err := intOpts.ClusterTaskWorkspaces(opt.clustertask); err != nil {
			return err
		}
		opt.Workspaces = append(opt.Workspaces, intOpts.Workspaces...)
	}

	return nil
}

func createPipelineResource(resType v1alpha1.PipelineResourceType, askOpt survey.AskOpt, p cli.Params, s *cli.Stream) (*v1alpha1.PipelineResource, error) {
	res := pipelineresource.Resource{
		AskOpts: askOpt,
		Params:  p,
		PipelineResource: v1alpha1.PipelineResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace()},
			Spec:       v1alpha1.PipelineResourceSpec{Type: resType},
		}}

	if err := res.AskMeta(); err != nil {
		return nil, err
	}

	resourceTypeParams := map[v1alpha1.PipelineResourceType]func() error{
		v1alpha1.PipelineResourceTypeGit:         res.AskGitParams,
		v1alpha1.PipelineResourceTypeStorage:     res.AskStorageParams,
		v1alpha1.PipelineResourceTypeImage:       res.AskImageParams,
		v1alpha1.PipelineResourceTypeCluster:     res.AskClusterParams,
		v1alpha1.PipelineResourceTypePullRequest: res.AskPullRequestParams,
		v1alpha1.PipelineResourceTypeCloudEvent:  res.AskCloudEventParams,
	}
	if res.PipelineResource.Spec.Type != "" {
		if err := resourceTypeParams[res.PipelineResource.Spec.Type](); err != nil {
			return nil, err
		}
	}
	cs, err := p.Clients()
	if err != nil {
		return nil, err
	}
	newRes, err := cs.Resource.TektonV1alpha1().PipelineResources(p.Namespace()).Create(context.Background(), &res.PipelineResource, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(s.Out, "New %s resource \"%s\" has been created\n", newRes.Spec.Type, newRes.Name)
	return newRes, nil
}
