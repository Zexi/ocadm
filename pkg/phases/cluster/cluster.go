package cluster

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	appv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog"
	kubeadmutil "k8s.io/kubernetes/cmd/kubeadm/app/util"
	kubeconfigutil "k8s.io/kubernetes/cmd/kubeadm/app/util/kubeconfig"

	operatorconstants "yunion.io/x/onecloud-operator/pkg/apis/constants"
	"yunion.io/x/onecloud-operator/pkg/apis/onecloud/v1alpha1"
	"yunion.io/x/onecloud-operator/pkg/client/clientset/versioned"
	occonfig "yunion.io/x/onecloud-operator/pkg/manager/config"

	"yunion.io/x/ocadm/pkg/apis/constants"
	apiv1 "yunion.io/x/ocadm/pkg/apis/v1"
	"yunion.io/x/ocadm/pkg/options"
	configutil "yunion.io/x/ocadm/pkg/util/config"
	"yunion.io/x/ocadm/pkg/util/kube"
	ocutil "yunion.io/x/ocadm/pkg/util/onecloud"
)

const (
	DefaultClusterName  = "default"
	DefaultOperatorName = "onecloud-operator"
)

type clusterData struct {
	cfg        *apiv1.InitConfiguration
	client     versioned.Interface
	k8sClient  kubernetes.Interface
	kubeClient *kube.Client
}

func newClusterData(cmd *cobra.Command, args []string) (*clusterData, error) {
	var tlsBootstrapCfg *clientcmdapi.Config
	var err error

	kubeConfigFile := constants.GetAdminKubeConfigPath()
	if _, err := os.Stat(kubeConfigFile); err == nil {
		// use the admin.conf as tlsBootstrapCfg, that is the kubeconfig file used for reading the ocadm-config during dicovery
		klog.V(1).Infof("[preflight] found %s. Use it for skipping discovery", kubeConfigFile)
		tlsBootstrapCfg, err = clientcmd.LoadFromFile(kubeConfigFile)
		if err != nil {
			return nil, errors.Wrapf(err, "Error loading %s", kubeConfigFile)
		}
	} else {
		return nil, err
	}

	if tlsBootstrapCfg == nil {
		return nil, errors.Errorf("Not found valid %s, please run this command at controlplane", kubeConfigFile)
	}

	cli, err := NewClusterClient(tlsBootstrapCfg)
	if err != nil {
		return nil, err
	}
	k8sCli, initCfg, err := FetchInitConfiguration(tlsBootstrapCfg)
	if err != nil {
		return nil, err
	}
	kubeCli, err := kube.NewClientByFile(kubeConfigFile)
	if err != nil {
		return nil, err
	}
	return &clusterData{
		cfg:        initCfg,
		client:     cli,
		k8sClient:  k8sCli,
		kubeClient: kubeCli,
	}, nil
}

func (data *clusterData) GetDefaultCluster() (*v1alpha1.OnecloudCluster, error) {
	cli := data.client
	ret, err := cli.OnecloudV1alpha1().OnecloudClusters(constants.OnecloudNamespace).Get(DefaultClusterName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (data *clusterData) GetOperator() (*appv1.Deployment, error) {
	cli := data.k8sClient
	return cli.AppsV1().Deployments(constants.OnecloudNamespace).Get(DefaultOperatorName, metav1.GetOptions{})
}

type createOptions struct {
	useEE   bool
	version string
	wait    bool
}

func newCreateOptions() *createOptions {
	return &createOptions{}
}

func NewCmdCreate(out io.Writer) *cobra.Command {
	opt := newCreateOptions()
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Run this command to create onecloud cluster",
		Run: func(cmd *cobra.Command, args []string) {
			data, err := newClusterData(cmd, args)
			kubeadmutil.CheckErr(err)

			oc, err := CreateCluster(data, opt)
			kubeadmutil.CheckErr(err)

			fmt.Fprintf(out, "Cluster %s created\n", oc.GetName())
		},
		Args: cobra.NoArgs,
	}
	AddCreateOptions(cmd.Flags(), opt)
	return cmd
}

func AddCreateOptions(flagSet *flag.FlagSet, opt *createOptions) {
	flagSet.BoolVar(&opt.useEE, "use-ee", opt.useEE, "Use EE edition")
	flagSet.StringVar(&opt.version, "version", opt.version, "onecloud cluster version")
	flagSet.BoolVar(&opt.wait, "wait", opt.wait, "wait until workload created")
}

func NewCmdConfig() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rcadmin",
		Short: "Get climc rc admin auth config",
		Run: func(cmd *cobra.Command, args []string) {
			data, err := newClusterData(cmd, args)
			kubeadmutil.CheckErr(err)

			ret, err := GetClusterRCAdmin(data)
			kubeadmutil.CheckErr(err)

			fmt.Printf("%s\n", ret)
		},
		Args: cobra.NoArgs,
	}
	return cmd
}

func CreateCluster(data *clusterData, opt *createOptions) (*v1alpha1.OnecloudCluster, error) {
	cli := data.client
	cfg := data.cfg
	ret, err := cli.OnecloudV1alpha1().OnecloudClusters(constants.OnecloudNamespace).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	if len(ret.Items) != 0 {
		return nil, errors.Errorf("Cluster already create")
	}
	oc, err := cli.OnecloudV1alpha1().OnecloudClusters(constants.OnecloudNamespace).Create(newCluster(cfg, opt))
	if err != nil {
		return nil, errors.Wrap(err, "create cluster")
	}
	if opt.wait {
		if err := ocutil.WaitOnecloudDeploymentUpdated(data.client, oc.GetName(), oc.GetNamespace(), 30*time.Minute); err != nil {
			return oc, errors.Wrap(err, "wait onecloud cluster services running")
		}
	}
	return oc, nil
}

func newCluster(cfg *apiv1.InitConfiguration, opt *createOptions) *v1alpha1.OnecloudCluster {
	lbEndpoint := cfg.ControlPlaneEndpoint
	if lbEndpoint != "" {
		lbEndpoint = strings.Split(lbEndpoint, ":")[0]
	}
	if lbEndpoint == "" {
		lbEndpoint = cfg.ManagementNetInterface.IPAddress()
	}
	oc := &v1alpha1.OnecloudCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: constants.OnecloudNamespace,
			Name:      DefaultClusterName,
		},
		Spec: v1alpha1.OnecloudClusterSpec{
			Mysql: v1alpha1.Mysql{
				Host:     cfg.MysqlConnection.Server,
				Port:     int32(cfg.MysqlConnection.Port),
				Username: cfg.MysqlConnection.Username,
				Password: cfg.MysqlConnection.Password,
			},
			LoadBalancerEndpoint: lbEndpoint,
			ImageRepository:      cfg.ImageRepository,
			Version:              cfg.OnecloudVersion,
			Region:               cfg.Region,
		},
	}
	if opt.version != "" {
		oc.Spec.Version = opt.version
	}
	if opt.useEE {
		ocutil.SetOCUseEE(oc)
	} else {
		ocutil.SetOCUseCE(oc)
	}
	return oc
}

func GetClusterRCAdmin(data *clusterData) (string, error) {
	cli := data.client
	cluster, err := cli.OnecloudV1alpha1().OnecloudClusters(constants.OnecloudNamespace).Get(DefaultClusterName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	cfg, err := occonfig.GetClusterConfigByClient(data.k8sClient, cluster)
	if err != nil {
		return "", err
	}
	keystonePort := cfg.Keystone.Port
	authURL := fmt.Sprintf("https://%s:%d/v3", cluster.Spec.LoadBalancerEndpoint, keystonePort)
	passwd := cluster.Spec.Keystone.BootstrapPassword
	return fmt.Sprintf(
		`export OS_AUTH_URL=%s
export OS_USERNAME=sysadmin
export OS_PASSWORD=%s
export OS_PROJECT_NAME=system
export YUNION_INSECURE=true
export OS_REGION_NAME=%s
export OS_ENDPOINT_TYPE=publicURL`, authURL, passwd, cluster.Spec.Region), nil
}

func NewClusterClient(config *clientcmdapi.Config) (*versioned.Clientset, error) {
	clientConfig, err := clientcmd.NewDefaultClientConfig(*config, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create API client configuration from kubeconfig")
	}
	return versioned.NewForConfig(clientConfig)
}

func FetchInitConfiguration(tlsBootstrapCfg *clientcmdapi.Config) (*kubernetes.Clientset, *apiv1.InitConfiguration, error) {
	// creates a client to access the cluster using the bootstrap token identity
	tlsClient, err := kubeconfigutil.ToClientSet(tlsBootstrapCfg)
	if err != nil {
		return nil, nil, errors.Wrap(err, "unable to access the cluster")
	}
	initconfiguration, err := configutil.FetchInitConfigurationFromCluster(tlsClient, ioutil.Discard, "preflight", true)
	if err != nil {
		return nil, nil, errors.Wrap(err, "unable to fetch the ocadm-config ConfigMap")
	}
	return tlsClient, initconfiguration, nil
}

type updateOptions struct {
	version         string
	operatorVersion string
	imageRepository string
	wait            bool
	useEE           bool
	useCE           bool
}

func newUpdateOptions() *updateOptions {
	return &updateOptions{}
}

func NewCmdUpdate(out io.Writer) *cobra.Command {
	opt := newUpdateOptions()
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Run this command to update onecloud cluster",
		Run: func(cmd *cobra.Command, args []string) {
			data, err := newClusterData(cmd, args)
			kubeadmutil.CheckErr(err)
			err = updateCluster(data, opt)
			kubeadmutil.CheckErr(err)
		},
		Args: cobra.NoArgs,
	}
	AddUpdateOptions(cmd.Flags(), opt)
	return cmd
}

func AddUpdateOptions(flagSet *flag.FlagSet, opt *updateOptions) {
	flagSet.StringVar(&opt.version, "version", opt.version, "onecloud cluster version")
	flagSet.StringVar(&opt.operatorVersion, options.OperatorVersion, opt.operatorVersion, "onecloud operator version")
	flagSet.StringVar(&opt.imageRepository, "image-repository", opt.imageRepository, "image registry repo")
	flagSet.BoolVar(&opt.wait, "wait", opt.wait, "wait until workload updated")
	flagSet.BoolVar(&opt.useEE, "use-ee", opt.useEE, "use enterprise edition onecloud")
	flagSet.BoolVar(&opt.useCE, "use-ce", opt.useCE, "use community edition onecloud")
}

func updateCluster(data *clusterData, opt *updateOptions) error {
	operator, err := data.GetOperator()
	if err != nil {
		return errors.Wrap(err, "get onecloud operator")
	}
	reg, imgName, version, err := getOperatorVersion(operator)
	if err != nil {
		return errors.Wrap(err, "get operator version")
	}
	if opt.operatorVersion != "" {
		if opt.operatorVersion != version {
			version = opt.operatorVersion
		}
	}
	if opt.imageRepository != "" {
		if opt.imageRepository != reg {
			reg = opt.imageRepository
		}
	}
	operator.Spec.Template.Spec.Containers[0].Image = fmt.Sprintf("%s/%s:%s", reg, imgName, version)
	if _, err := data.k8sClient.AppsV1().Deployments(constants.OnecloudNamespace).Update(operator); err != nil {
		return errors.Wrap(err, "update operator")
	}
	if opt.wait {
		rollout, err := data.kubeClient.Rollout()
		if err != nil {
			return errors.Wrap(err, "get rollout cmd")
		}
		if err := rollout.Status(0).
			SetNamespace(constants.OnecloudNamespace).
			RunDeployment(operator.GetName()); err != nil {
			return err
		}
	}

	oc, err := data.GetDefaultCluster()
	if err != nil {
		return errors.Wrap(err, "get default onecloud cluster")
	}
	updateOC := false
	if oc.Annotations == nil {
		oc.Annotations = make(map[string]string)
	}
	edition := oc.Annotations[operatorconstants.OnecloudEditionAnnotationKey]
	if opt.useEE && edition != operatorconstants.OnecloudEnterpriseEdition {
		oc.Annotations[operatorconstants.OnecloudEditionAnnotationKey] = operatorconstants.OnecloudEnterpriseEdition
		updateOC = true
	} else if opt.useCE && edition != operatorconstants.OnecloudCommunityEdition {
		oc.Annotations[operatorconstants.OnecloudEditionAnnotationKey] = operatorconstants.OnecloudCommunityEdition
		updateOC = true
	}
	if opt.version != "" {
		if opt.version != oc.Spec.Version {
			oc.Spec.Version = opt.version
		}
		updateOC = true
	}
	if opt.imageRepository != "" {
		if opt.imageRepository != oc.Spec.ImageRepository {
			oc.Spec.ImageRepository = opt.imageRepository
		}
		updateOC = true
	}
	if updateOC {
		if _, err := data.client.OnecloudV1alpha1().OnecloudClusters(constants.OnecloudNamespace).Update(oc); err != nil {
			return errors.Wrap(err, "update default onecloud cluster")
		}
		if opt.wait {
			if err := ocutil.WaitOnecloudDeploymentUpdated(data.client, oc.GetName(), oc.GetNamespace(), 5*time.Minute); err != nil {
				return errors.Wrap(err, "wait onecloud cluster updated")
			}
			rollout, err := data.kubeClient.Rollout()
			if err != nil {
				return errors.Wrap(err, "get rollout cmd")
			}
			if err := rollout.Status(0).
				SetNamespace(constants.OnecloudNamespace).
				RunDeployment(fmt.Sprintf("%s-web", oc.GetName())); err != nil {
				return err
			}
		}
	}
	return nil
}

func getRepoImageName(img string) (string, string, string) {
	parts := strings.Split(img, "/")
	var (
		repo      string
		imageName string
		tag       string
	)
	getImageTag := func(img string) (string, string) {
		parts := strings.Split(img, ":")
		if len(parts) == 0 {
			return "", ""
		}
		if len(parts) == 1 {
			tag := "latest"
			img := parts[0]
			return img, tag
		}
		img = parts[0]
		tag = parts[len(parts)-1]
		if strings.HasSuffix(img, "@sha256") {
			rets := strings.Split(img, "@sha256")
			img = rets[0]
			tag = fmt.Sprintf("@sha256:%s", tag)
		}
		return img, tag
	}
	getRepo := func(parts []string) string {
		return filepath.Join(parts...)
	}
	if len(parts) == 0 {
		return "", "", ""
	}
	if len(parts) == 1 {
		imageName, tag = getImageTag(parts[0])
	} else {
		imageName, tag = getImageTag(parts[len(parts)-1])
		repo = getRepo(parts[0 : len(parts)-1])
	}
	return repo, imageName, tag
}

func getOperatorVersion(operator *appv1.Deployment) (string, string, string, error) {
	img := operator.Spec.Template.Spec.Containers[0].Image
	repo, imageName, tag := getRepoImageName(img)
	if repo == "" {
		return "", "", "", errors.Errorf("Failed to get %q repo", img)
	}
	if imageName == "" {
		return "", "", "", errors.Errorf("Failed to get %q image name", img)
	}
	if tag == "" {
		return "", "", "", errors.Errorf("Failed to get %q tag", img)
	}
	return repo, imageName, tag, nil
}
