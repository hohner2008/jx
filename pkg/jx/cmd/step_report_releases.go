package cmd

import (
	"io"

	"time"

	"fmt"

	"github.com/jenkins-x/jx/pkg/apis/jenkins.io/v1"
	"github.com/jenkins-x/jx/pkg/client/clientset/versioned"
	"github.com/jenkins-x/jx/pkg/jx/cmd/log"
	"github.com/jenkins-x/jx/pkg/jx/cmd/templates"
	cmdutil "github.com/jenkins-x/jx/pkg/jx/cmd/util"
	"github.com/jenkins-x/jx/pkg/kube"
	pe "github.com/jenkins-x/jx/pkg/pipeline_events"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
)

// StepReportReleasesOptions contains the command line flags
type StepReportReleasesOptions struct {
	StepReportOptions
	Watch bool
	pe.PipelineEventsProvider
}

var (
	StepReportReleasesLong = templates.LongDesc(`
		This pipeline step reports releases to pluggable backends like ElasticSearch
`)

	StepReportReleasesExample = templates.Examples(`
		jx step report Releases
`)
)

func NewCmdStepReportReleases(f cmdutil.Factory, out io.Writer, errOut io.Writer) *cobra.Command {
	options := StepReportReleasesOptions{
		StepReportOptions: StepReportOptions{
			StepOptions: StepOptions{
				CommonOptions: CommonOptions{
					Factory: f,
					Out:     out,
					Err:     errOut,
				},
			},
		},
	}
	cmd := &cobra.Command{
		Use:     "releases",
		Short:   "Reports Releases",
		Long:    StepReportReleasesLong,
		Example: StepReportReleasesExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Cmd = cmd
			options.Args = args
			err := options.Run()
			cmdutil.CheckErr(err)
		},
	}

	cmd.Flags().BoolVarP(&options.Watch, "watch", "w", false, "Whether to watch Releases")
	options.addCommonFlags(cmd)
	return cmd
}

func (o *StepReportReleasesOptions) Run() error {

	// look up services that we want to send events to using a label?

	// watch Releases and send an event for each backend i.e elasticsearch
	f := o.Factory

	_, _, err := o.KubeClient()
	if err != nil {
		return fmt.Errorf("cannot connect to kubernetes cluster: %v", err)
	}

	jxClient, _, err := o.Factory.CreateJXClient()
	if err != nil {
		return fmt.Errorf("cannot create jx client: %v", err)
	}

	apisClient, err := f.CreateApiExtensionsClient()
	if err != nil {
		return err
	}
	err = kube.RegisterReleaseCRD(apisClient)
	if err != nil {
		return err
	}

	externalURL, err := o.ensureAddonServiceAvailable(esServiceName)
	if err != nil {
		log.Warnf("no %s service found, are you in your teams dev environment?  Type `jx env` to switch.\n", esServiceName)
		return fmt.Errorf("try running `jx create addon pipeline-events` in your teams dev environment: %v", err)
	}

	server, auth, err := o.CommonOptions.getAddonAuthByKind(kube.ValueKindRelease, externalURL)
	if err != nil {
		return fmt.Errorf("error getting %s auth details, %v", kube.ValueKindRelease, err)
	}

	o.PipelineEventsProvider, err = pe.NewElasticsearchProvider(server, auth)
	if err != nil {
		return fmt.Errorf("error creating elasticsearch provider, %v", err)
	}

	err = o.watchPipelineReleases(jxClient, o.currentNamespace)
	if err != nil {
		return err
	}

	return nil
}

func (o *StepReportReleasesOptions) watchPipelineReleases(jxClient *versioned.Clientset, ns string) error {

	activity := &v1.PipelineActivity{}
	listWatch := cache.NewListWatchFromClient(jxClient.JenkinsV1().RESTClient(), "release", ns, fields.Everything())
	kube.SortListWatchByName(listWatch)
	_, controller := cache.NewInformer(
		listWatch,
		activity,
		time.Hour*24,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				// send to registered backends
				release, ok := obj.(*v1.Release)
				if !ok {
					log.Errorf("Object is not a Release %#v\n", obj)
					return
				}
				log.Infof("New activity added %s\n", release.ObjectMeta.Name)
				err := o.PipelineEventsProvider.SendRelease(release)
				if err != nil {
					log.Errorf("%v\n", err)
					return
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				release, ok := newObj.(*v1.Release)
				if !ok {
					log.Errorf("Object is not a PipelineActivity %#v\n", newObj)
					return
				}
				log.Infof("Updated activity added %s\n", activity.ObjectMeta.Name)

				err := o.PipelineEventsProvider.SendRelease(release)
				if err != nil {
					log.Errorf("%v\n", err)
					return
				}
			},
			DeleteFunc: func(obj interface{}) {
				// no need to send event

			},
		},
	)

	stop := make(chan struct{})
	go controller.Run(stop)

	// Wait forever
	select {}
}
